package tools

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// wait_for: block until a condition holds, instead of polling from the agent.
//
// Ten scripts under models/apex-runs and four more written during one debugging
// session each re-implement the same loop. What they wait on is a short list:
// a port (28 occurrences), MemAvailable (10), a PID to exit (6), a process by
// pattern (4), a file to stop growing (2).
//
// The reason to make it a tool rather than a snippet is CONTEXT, not
// convenience. An agent that polls by calling capture_pane ten times spends ten
// inference passes and leaves ten results in its history; the same wait here is
// one call and one result. The loop leaves the model's context entirely.
//
// Two lessons are baked in rather than left to the caller:
//
//   - process_exit takes a PID, never a pattern. `pgrep -f <pattern>` matches
//     the invoking shell's own command line, so a wait loop built on it can
//     never finish and a kill built on it kills the caller. That cost six
//     failures in one session. A tool that accepted patterns would reproduce it.
//
//   - A freed PORT is not freed MEMORY. On unified memory a large model's pages
//     outlive its listening socket by many seconds, and starting the next model
//     into that window makes the NVIDIA allocator spin until the machine is
//     power-cycled. port_listening(gone) says so in its own output, and
//     memory_free is the condition that actually guards a model launch.

const (
	defaultWaitTimeout  = 300
	maxWaitTimeout      = 3600
	defaultPollInterval = 2 * time.Second
	defaultQuietSeconds = 15
)

// waitCondition evaluates once. It returns whether the condition holds and a
// short description of what was observed, which becomes the tool's answer —
// "MemAvailable 115000 MB" is a more useful result than "true".
type waitCondition func() (bool, string, error)

func (m *miscTools) registerWait(r *Registry) {
	r.Register(&Tool{
		Name: "wait_for",
		Description: "Block until a condition is met, then return. Use this instead of polling in a loop — " +
			"it waits server-side, so one call replaces many check-and-sleep round trips. " +
			"Conditions: process_exit (pid), memory_free (mb), port_listening (port, gone), " +
			"http_ok (url), file_stable (path), command_succeeds (command). Always bounded by a timeout.",
		Schema:   waitForSchema,
		ReadOnly: true, // observes; command_succeeds runs a caller-supplied command
		Handler:  m.waitFor,
	})
}

var waitForSchema = map[string]interface{}{
	"type": "object",
	"properties": map[string]interface{}{
		"condition": map[string]interface{}{
			"type": "string",
			"description": "What to wait for: 'process_exit' (pid gone), 'memory_free' (MemAvailable >= mb), " +
				"'port_listening' (port accepting, or freed when gone=true), 'http_ok' (url returns 2xx), " +
				"'file_stable' (path exists and stops changing size), 'command_succeeds' (command exits 0).",
			"enum": waitConditions,
		},
		"pid":           map[string]interface{}{"type": "integer", "description": "process_exit: the PID to wait for. A PID, never a name or pattern."},
		"mb":            map[string]interface{}{"type": "integer", "description": "memory_free: megabytes of MemAvailable required."},
		"port":          map[string]interface{}{"type": "integer", "description": "port_listening: TCP port."},
		"host":          map[string]interface{}{"type": "string", "description": "port_listening: host (default 127.0.0.1)."},
		"gone":          map[string]interface{}{"type": "boolean", "description": "port_listening: wait for the port to be FREE rather than listening."},
		"url":           map[string]interface{}{"type": "string", "description": "http_ok: URL to poll until it returns 2xx."},
		"path":          map[string]interface{}{"type": "string", "description": "file_stable: file to wait on."},
		"quiet_seconds": map[string]interface{}{"type": "integer", "description": "file_stable: seconds the size must stay unchanged (default 15)."},
		"command":       map[string]interface{}{"type": "string", "description": "command_succeeds: shell command polled until it exits 0."},
		"timeout_seconds": map[string]interface{}{
			"type":        "integer",
			"description": "Give up after this long (default 300, max 3600). Waiting is always bounded.",
		},
		"poll_seconds": map[string]interface{}{"type": "integer", "description": "Seconds between checks (default 2)."},
	},
	"required": []string{"condition"},
}

var waitConditions = []string{
	"process_exit", "memory_free", "port_listening", "http_ok", "file_stable", "command_succeeds",
}

func (m *miscTools) waitFor(ctx context.Context, a map[string]interface{}) (string, error) {
	cond := strings.ToLower(strings.TrimSpace(argString(a, "condition")))
	check, label, err := buildWaitCondition(cond, a)
	if err != nil {
		return "", err
	}

	timeout := argInt(a, "timeout_seconds", defaultWaitTimeout)
	if timeout <= 0 {
		timeout = defaultWaitTimeout
	}
	if timeout > maxWaitTimeout {
		timeout = maxWaitTimeout
	}
	poll := defaultPollInterval
	if p := argInt(a, "poll_seconds", 0); p > 0 {
		poll = time.Duration(p) * time.Second
	}

	start := time.Now()
	deadline := start.Add(time.Duration(timeout) * time.Second)

	// Check once before sleeping: a condition that is already true should cost
	// no wall time at all.
	for {
		ok, observed, err := check()
		if err != nil {
			return "", fmt.Errorf("waiting for %s: %w", label, err)
		}
		if ok {
			return fmt.Sprintf("Condition met after %s: %s (%s).",
				roundDur(time.Since(start)), label, observed), nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Sprintf("TIMED OUT after %s waiting for %s. Last observed: %s. "+
				"The condition was not met — do not assume it succeeded.",
				roundDur(time.Since(start)), label, observed), nil
		}
		select {
		case <-ctx.Done():
			return fmt.Sprintf("Cancelled after %s waiting for %s. Last observed: %s.",
				roundDur(time.Since(start)), label, observed), nil
		case <-time.After(poll):
		}
	}
}

// buildWaitCondition validates arguments up front, so a missing pid fails
// immediately rather than after a 300-second wait on a condition that could
// never be evaluated.
func buildWaitCondition(cond string, a map[string]interface{}) (waitCondition, string, error) {
	switch cond {
	case "process_exit":
		pid := argInt(a, "pid", 0)
		if pid <= 0 {
			return nil, "", fmt.Errorf("process_exit requires a positive 'pid'. " +
				"Pass a PID, not a process name: a pattern match would also match the " +
				"caller's own command line and never finish")
		}
		return func() (bool, string, error) {
			if processAlive(pid) {
				return false, fmt.Sprintf("pid %d still running", pid), nil
			}
			return true, fmt.Sprintf("pid %d has exited", pid), nil
		}, fmt.Sprintf("pid %d to exit", pid), nil

	case "memory_free":
		want := argInt(a, "mb", 0)
		if want <= 0 {
			return nil, "", fmt.Errorf("memory_free requires a positive 'mb'")
		}
		return func() (bool, string, error) {
			avail, err := memAvailableMB()
			if err != nil {
				return false, "", err
			}
			return avail >= want, fmt.Sprintf("MemAvailable %d MB of %d MB required", avail, want), nil
		}, fmt.Sprintf("%d MB to be free", want), nil

	case "port_listening":
		port := argInt(a, "port", 0)
		if port <= 0 || port > 65535 {
			return nil, "", fmt.Errorf("port_listening requires a valid 'port'")
		}
		host := argString(a, "host")
		if host == "" {
			host = "127.0.0.1"
		}
		gone := argBool(a, "gone", false)
		addr := net.JoinHostPort(host, strconv.Itoa(port))
		label := fmt.Sprintf("%s to accept connections", addr)
		if gone {
			label = fmt.Sprintf("%s to stop listening", addr)
		}
		return func() (bool, string, error) {
			listening := portListening(addr)
			state := "listening"
			if !listening {
				state = "not listening"
			}
			if gone && !listening {
				// The distinction that wedged this box once already.
				return true, state + " — note a freed port does NOT mean freed memory; " +
					"use memory_free before launching a model", nil
			}
			if !gone && listening {
				return true, state, nil
			}
			return false, state, nil
		}, label, nil

	case "http_ok":
		url := argString(a, "url")
		if url == "" {
			return nil, "", fmt.Errorf("http_ok requires a 'url'")
		}
		return func() (bool, string, error) {
			code, err := httpStatus(url)
			if err != nil {
				return false, "no response yet (" + err.Error() + ")", nil
			}
			return code >= 200 && code < 300, fmt.Sprintf("HTTP %d", code), nil
		}, url + " to return 2xx", nil

	case "file_stable":
		path := argString(a, "path")
		if path == "" {
			return nil, "", fmt.Errorf("file_stable requires a 'path'")
		}
		quiet := argInt(a, "quiet_seconds", defaultQuietSeconds)
		if quiet <= 0 {
			quiet = defaultQuietSeconds
		}
		var lastSize int64 = -1
		var stableSince time.Time
		return func() (bool, string, error) {
			fi, err := os.Stat(path)
			if err != nil {
				lastSize = -1
				return false, "does not exist yet", nil
			}
			size := fi.Size()
			now := time.Now()
			if size != lastSize {
				lastSize = size
				stableSince = now
				return false, fmt.Sprintf("%.1f MB and still changing", float64(size)/(1024*1024)), nil
			}
			held := now.Sub(stableSince)
			if held >= time.Duration(quiet)*time.Second {
				return true, fmt.Sprintf("%.1f MB, unchanged for %s", float64(size)/(1024*1024), roundDur(held)), nil
			}
			return false, fmt.Sprintf("%.1f MB, unchanged for %s of %ds", float64(size)/(1024*1024), roundDur(held), quiet), nil
		}, fmt.Sprintf("%s to stop changing", path), nil

	case "command_succeeds":
		command := argString(a, "command")
		if command == "" {
			return nil, "", fmt.Errorf("command_succeeds requires a 'command'")
		}
		return func() (bool, string, error) {
			cmd := exec.Command("sh", "-c", command)
			out, err := cmd.CombinedOutput()
			tail := strings.TrimSpace(string(out))
			if len(tail) > 200 {
				tail = tail[len(tail)-200:]
			}
			if err != nil {
				return false, fmt.Sprintf("exit %d: %s", exitCodeOf(err), tail), nil
			}
			return true, fmt.Sprintf("exit 0: %s", tail), nil
		}, "`" + command + "` to exit 0", nil
	}

	return nil, "", fmt.Errorf("unknown condition %q: expected one of %s",
		cond, strings.Join(waitConditions, ", "))
}

// processAlive reports whether a PID exists. Signal 0 performs the permission
// and existence checks without delivering anything.
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	// EPERM means it exists but belongs to someone else — still alive.
	return err == syscall.EPERM
}

// memAvailableMB reads MemAvailable, the only figure that means anything on a
// unified-memory box: it covers CPU and GPU and everything else running.
func memAvailableMB() (int, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "MemAvailable:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			break
		}
		kb, err := strconv.Atoi(fields[1])
		if err != nil {
			return 0, err
		}
		return kb / 1024, nil
	}
	return 0, fmt.Errorf("MemAvailable not found in /proc/meminfo")
}

func portListening(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 800*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func httpStatus(url string) (int, error) {
	cl := &http.Client{Timeout: 5 * time.Second}
	resp, err := cl.Get(url)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}

func exitCodeOf(err error) int {
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	return -1
}

// roundDur keeps elapsed times readable: 47s, not 47.0132910s.
func roundDur(d time.Duration) time.Duration {
	if d < time.Minute {
		return d.Round(time.Second)
	}
	return d.Round(time.Second)
}
