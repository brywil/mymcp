package tools

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// --- tmux manager (ported from goclaw internal/tmux) ---

// tmuxSession represents a tmux session.
type tmuxSession struct {
	Name string
	PID  int
}

// tmuxManager manages tmux sessions via the CLI.
type tmuxManager struct {
	SocketPath string
}

// tmuxArgs builds the argument list for a tmux command. When SocketPath is
// empty, -S is omitted so tmux uses the default server.
func (m *tmuxManager) tmuxArgs(args ...string) []string {
	if m.SocketPath != "" {
		return append([]string{"-S", m.SocketPath}, args...)
	}
	return args
}

// ListSessions returns all active tmux sessions.
func (m *tmuxManager) ListSessions() ([]tmuxSession, error) {
	cmd := exec.Command("tmux", m.tmuxArgs("list-sessions")...)
	output, err := cmd.Output()
	if err != nil {
		// tmux exits 1 when no sessions are running — not an error.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return []tmuxSession{}, nil
		}
		return nil, fmt.Errorf("failed to list tmux sessions: %w", err)
	}

	var sessions []tmuxSession
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Format: "session_name: N windows (created ...)"; split on first colon.
		if idx := strings.Index(line, ":"); idx > 0 {
			sessions = append(sessions, tmuxSession{Name: strings.TrimSpace(line[:idx])})
		}
	}
	return sessions, nil
}

// SessionExists reports whether a session with the given name is running.
func (m *tmuxManager) SessionExists(name string) (bool, error) {
	cmd := exec.Command("tmux", m.tmuxArgs("has-session", "-t", name)...)
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return false, nil
		}
		return false, fmt.Errorf("error checking session existence: %w", err)
	}
	return true, nil
}

// CreateSession creates a new tmux session, optionally running a command.
func (m *tmuxManager) CreateSession(name, dir, command string) (*tmuxSession, error) {
	if exists, err := m.SessionExists(name); err != nil {
		return nil, err
	} else if exists {
		return nil, fmt.Errorf("tmux session '%s' already exists", name)
	}

	args := m.tmuxArgs("new-session", "-d", "-s", name, "-c", dir)
	out, err := exec.Command("tmux", args...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		if strings.Contains(msg, "already exists") {
			return nil, fmt.Errorf("tmux session '%s' already exists", name)
		}
		return nil, fmt.Errorf("failed to create tmux session %s: %s", name, msg)
	}

	// Retry with backoff — tmux needs a moment to register the session.
	for i := 0; i < 10; i++ {
		time.Sleep(200 * time.Millisecond)
		sessions, err := m.ListSessions()
		if err != nil {
			return nil, err
		}
		for _, s := range sessions {
			if s.Name == name {
				if command != "" {
					time.Sleep(300 * time.Millisecond)
					if err := m.SendKeys(name, command, true); err != nil {
						log.Printf("Warning: failed to send command to session %s: %v", name, err)
					}
				}
				return &s, nil
			}
		}
	}
	return nil, fmt.Errorf("session %s created but not found in list after retries", name)
}

// KillSession terminates a tmux session.
func (m *tmuxManager) KillSession(name string) error {
	out, err := exec.Command("tmux", m.tmuxArgs("kill-session", "-t", name)...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("failed to kill tmux session %s: %s", name, msg)
	}
	return nil
}

// SendKeys sends keystrokes to a running session, optionally pressing Enter.
func (m *tmuxManager) SendKeys(name, keys string, enter bool) error {
	if keys != "" {
		out, err := exec.Command("tmux", m.tmuxArgs("send-keys", "-l", "-t", name, keys)...).CombinedOutput()
		if err != nil {
			msg := strings.TrimSpace(string(out))
			if msg == "" {
				msg = err.Error()
			}
			return fmt.Errorf("failed to send keys to tmux session %s: %s", name, msg)
		}
	}
	if enter {
		out, err := exec.Command("tmux", m.tmuxArgs("send-keys", "-t", name, "Enter")...).CombinedOutput()
		if err != nil {
			msg := strings.TrimSpace(string(out))
			if msg == "" {
				msg = err.Error()
			}
			return fmt.Errorf("failed to send Enter to tmux session %s: %s", name, msg)
		}
	}
	return nil
}

// CapturePane returns a session's pane content. historyLines <= 0 captures just
// the VISIBLE pane (the current screen — prompt + recent output); >0 additionally
// includes that many lines of scrollback above the screen. It deliberately does
// NOT default to the full history (-S -): on a session with a large scrollback
// that buried the current prompt under thousands of stale lines, so the caller
// could never see the present state.
func (m *tmuxManager) CapturePane(name string, historyLines int) (string, error) {
	args := []string{"capture-pane", "-t", name, "-p"}
	if historyLines > 0 {
		args = append(args, "-S", "-"+strconv.Itoa(historyLines))
	}
	out, err := exec.Command("tmux", m.tmuxArgs(args...)...).Output()
	if err != nil {
		return "", fmt.Errorf("failed to capture pane from tmux session %s: %w", name, err)
	}
	return string(out), nil
}

// runCmdSeq gives each RunCommand call a unique sentinel.
var runCmdSeq uint64

// defaultRunTimeout bounds how long RunCommand waits for completion.
const defaultRunTimeout = 60 * time.Second

// RunCommand runs a command in a session and, when enter is true, waits for it
// to finish (detected via a unique sentinel echoed with the exit code).
func (m *tmuxManager) RunCommand(name, command string, enter bool, timeoutSec int) (string, error) {
	if err := m.sendLiteral(name, command); err != nil {
		return "", err
	}
	if !enter {
		time.Sleep(300 * time.Millisecond)
		return m.CapturePane(name, 0)
	}
	if err := m.sendEnter(name); err != nil {
		return "", err
	}

	n := atomic.AddUint64(&runCmdSeq, 1)
	marker := fmt.Sprintf("__GOCLAW_DONE_%d_%d__", time.Now().UnixNano(), n)
	sentinel := fmt.Sprintf("printf '\\n%s%%s\\n' \"$?\"", marker)
	if err := m.sendLiteral(name, sentinel); err != nil {
		return "", err
	}
	if err := m.sendEnter(name); err != nil {
		return "", err
	}

	timeout := time.Duration(timeoutSec) * time.Second
	if timeoutSec <= 0 {
		timeout = defaultRunTimeout
	}
	doneRe := regexp.MustCompile(regexp.QuoteMeta(marker) + `(\d+)`)

	deadline := time.Now().Add(timeout)
	const pollInterval = 300 * time.Millisecond
	// Detect completion on the visible pane (the sentinel is the last thing
	// printed, so it lands at the bottom). Once found — or on timeout — grab a
	// bounded slice of history so output that scrolled off-screen is still
	// returned, without dumping the entire scrollback.
	for {
		time.Sleep(pollInterval)
		pane, err := m.CapturePane(name, 0)
		if err != nil {
			return "", err
		}
		if doneRe.MatchString(pane) {
			full, _ := m.CapturePane(name, 400)
			loc := doneRe.FindStringSubmatchIndex(full)
			exitCode := full[loc[2]:loc[3]]
			return trimRunOutput(full, marker, exitCode, false), nil
		}
		if time.Now().After(deadline) {
			full, _ := m.CapturePane(name, 400)
			return trimRunOutput(full, marker, "", true), nil
		}
	}
}

// sendLiteral types text into a session without pressing Enter.
func (m *tmuxManager) sendLiteral(name, text string) error {
	if out, err := exec.Command("tmux", m.tmuxArgs("send-keys", "-l", "-t", name, text)...).CombinedOutput(); err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("failed to send command to tmux session %s: %s", name, msg)
	}
	return nil
}

// sendEnter presses Enter in a session.
func (m *tmuxManager) sendEnter(name string) error {
	if out, err := exec.Command("tmux", m.tmuxArgs("send-keys", "-t", name, "Enter")...).CombinedOutput(); err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("failed to send Enter to tmux session %s: %s", name, msg)
	}
	return nil
}

// trimRunOutput cleans a captured pane for RunCommand and appends a status footer.
func trimRunOutput(pane, marker, exitCode string, timedOut bool) string {
	lines := strings.Split(pane, "\n")
	kept := make([]string, 0, len(lines))
	for _, ln := range lines {
		if strings.Contains(ln, marker) {
			if !timedOut {
				break
			}
			continue
		}
		if strings.Contains(ln, "__GOCLAW_DONE_") {
			continue
		}
		kept = append(kept, ln)
	}
	out := strings.TrimRight(strings.Join(kept, "\n"), "\n")
	if timedOut {
		return out + "\n\n[still running — command did not finish within the timeout. If this is a server or other long-running process, it may be fine; check with capture_pane or a port/health check rather than run_command.]"
	}
	return out + fmt.Sprintf("\n\n[command finished, exit code %s]", exitCode)
}

// RenameSession renames a tmux session.
func (m *tmuxManager) RenameSession(oldName, newName string) error {
	out, err := exec.Command("tmux", m.tmuxArgs("rename-session", oldName, newName)...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("failed to rename tmux session from %s to %s: %s", oldName, newName, msg)
	}
	return nil
}

// SuspendSession suspends a tmux session.
func (m *tmuxManager) SuspendSession(name string) error {
	out, err := exec.Command("tmux", m.tmuxArgs("suspend-session", "-t", name)...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("failed to suspend tmux session %s: %s", name, msg)
	}
	return nil
}

// ResumeSession resumes a suspended tmux session, optionally running a command.
func (m *tmuxManager) ResumeSession(name, command string) error {
	var cmd *exec.Cmd
	if command != "" {
		cmd = exec.Command("tmux", m.tmuxArgs("attach-session", "-t", name, "-d", "run-shell", command)...)
	} else {
		cmd = exec.Command("tmux", m.tmuxArgs("attach-session", "-t", name, "-d")...)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("failed to resume tmux session %s: %s", name, msg)
	}
	return nil
}

// --- tmux tools ---

// tmuxTools provides tmux session-management tools. It tracks an active session
// so name-less operations (send_keys, capture_pane, run_command) have a target.
type tmuxTools struct {
	mgr *tmuxManager

	mu            sync.Mutex
	activeSession string
}

// newTmuxTools builds a tmuxTools backed by a manager on the given socket path
// (empty socket path => default tmux server).
func newTmuxTools(socketPath string) *tmuxTools {
	return &tmuxTools{mgr: &tmuxManager{SocketPath: socketPath}}
}

func (t *tmuxTools) setActive(name string) {
	t.mu.Lock()
	t.activeSession = name
	t.mu.Unlock()
}

func (t *tmuxTools) getActive() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.activeSession
}

func (t *tmuxTools) clearActiveIf(name string) {
	t.mu.Lock()
	if t.activeSession == name {
		t.activeSession = ""
	}
	t.mu.Unlock()
}

// resolveName returns the session to operate on. A non-empty "name" is used and
// becomes active; otherwise the current active session is used.
func (t *tmuxTools) resolveName(a map[string]interface{}) (string, error) {
	if name := argString(a, "name"); name != "" {
		t.setActive(name)
		return name, nil
	}
	if active := t.getActive(); active != "" {
		return active, nil
	}
	return "", fmt.Errorf("no session specified and no active session set; create a session or set one active first")
}

// truncate caps output at maxLen runes with a dropped-count note.
// tmuxTruncate keeps the LAST maxLen chars. Terminal output is read tail-first —
// the current prompt and a command's result are at the end, so truncating the
// head (as this once did) discarded exactly what the caller needed to see.
func tmuxTruncate(s string, maxLen int) string {
	r := []rune(s)
	if len(r) <= maxLen {
		return s
	}
	return "... (truncated, " + strconv.Itoa(len(r)-maxLen) + " earlier chars)\n\n" + string(r[len(r)-maxLen:])
}

func (t *tmuxTools) register(r *Registry) {
	r.Register(&Tool{
		Name:        "list_sessions",
		Description: "List all active tmux sessions.",
		Schema:      obj(map[string]interface{}{}),
		ReadOnly:    true,
		Handler:     t.listSessions,
	})
	r.Register(&Tool{
		Name:        "set_active_session",
		Description: "Make an existing tmux session the active one so name-less commands target it.",
		Schema: obj(map[string]interface{}{
			"name": strProp("Name of the existing tmux session to make active"),
		}, "name"),
		ReadOnly: false,
		Handler:  t.setActiveSession,
	})
	r.Register(&Tool{
		Name:        "create_session",
		Description: "Create a new tmux session.",
		Schema: obj(map[string]interface{}{
			"name":      strProp("Name for the new tmux session"),
			"directory": strProp("Working directory for the session (default: current directory)"),
			"command":   strProp("Optional command to run inside the session on creation"),
		}, "name"),
		ReadOnly: false,
		Handler:  t.createSession,
	})
	r.Register(&Tool{
		Name:        "kill_session",
		Description: "Kill a tmux session.",
		Schema: obj(map[string]interface{}{
			"name": strProp("Name of the tmux session to kill"),
		}, "name"),
		ReadOnly: false,
		Handler:  t.killSession,
	})
	r.Register(&Tool{
		Name:        "send_keys",
		Description: "Send keystrokes to a tmux session.",
		Schema: obj(map[string]interface{}{
			"name": strProp("Name of the tmux session. Optional; defaults to the active session. Providing it makes that session active."),
			"keys": strProp("Keys to send. Enter is sent automatically after the keys so commands execute."),
			"enter": map[string]interface{}{
				"type":        "boolean",
				"description": "Whether to send Enter after the keys. Defaults to true. Set to false to type text without executing (e.g., editing a file).",
			},
		}, "keys"),
		ReadOnly: false,
		Handler:  t.sendKeys,
	})
	r.Register(&Tool{
		Name:        "capture_pane",
		Description: "Capture a tmux session's pane. By default returns the VISIBLE screen (current prompt + recent output). Set 'lines' to also include that many lines of scrollback above the screen.",
		Schema: obj(map[string]interface{}{
			"name": strProp("Name of the tmux session. Optional; defaults to the active session. Providing it makes that session active."),
			"lines": map[string]interface{}{
				"type":        "integer",
				"description": "Extra scrollback lines to include above the visible screen (default 0 = visible pane only). Use e.g. 200 to see more history.",
			},
		}),
		ReadOnly: true,
		Handler:  t.capturePane,
	})
	r.Register(&Tool{
		Name:        "run_command",
		Description: "Run a command inside a tmux session and wait for it to finish, returning its output and exit code.",
		Schema: obj(map[string]interface{}{
			"name":    strProp("Name of the tmux session. Optional; defaults to the active session. Providing it makes that session active."),
			"command": strProp("Command to run inside the session. Enter is sent automatically and the tool WAITS for the command to finish, then returns its output and exit code. For a process that never exits (e.g. launching a server), it will report 'still running' at the timeout — that's expected; verify such processes with capture_pane or a port/health check instead."),
			"enter": map[string]interface{}{
				"type":        "boolean",
				"description": "Whether to send Enter after the command. Defaults to true. Set to false to type text without executing (e.g., editing a file).",
			},
			"timeout_seconds": map[string]interface{}{
				"type":        "integer",
				"description": "How long to wait for the command to finish before returning 'still running'. Defaults to 60. Raise it for slow commands like a large 'pip install' or a build.",
			},
		}, "command"),
		ReadOnly: false,
		Handler:  t.runCommand,
	})
	r.Register(&Tool{
		Name:        "rename_session",
		Description: "Rename a tmux session.",
		Schema: obj(map[string]interface{}{
			"old_name": strProp("Current name of the tmux session"),
			"new_name": strProp("New name for the tmux session"),
		}, "old_name", "new_name"),
		ReadOnly: false,
		Handler:  t.renameSession,
	})
	r.Register(&Tool{
		Name:        "suspend_session",
		Description: "Suspend a tmux session.",
		Schema: obj(map[string]interface{}{
			"name": strProp("Name of the tmux session to suspend"),
		}, "name"),
		ReadOnly: false,
		Handler:  t.suspendSession,
	})
	r.Register(&Tool{
		Name:        "resume_session",
		Description: "Resume a suspended tmux session.",
		Schema: obj(map[string]interface{}{
			"name":    strProp("Name of the tmux session to resume"),
			"command": strProp("Optional command to run after resuming"),
		}, "name"),
		ReadOnly: false,
		Handler:  t.resumeSession,
	})
}

func (t *tmuxTools) listSessions(ctx context.Context, a map[string]interface{}) (string, error) {
	sessions, err := t.mgr.ListSessions()
	if err != nil {
		return "", fmt.Errorf("listing tmux sessions: %w", err)
	}
	if len(sessions) == 0 {
		return "No tmux sessions are currently running.", nil
	}

	active := t.getActive()
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Active tmux sessions (%d):\n\n", len(sessions)))
	for _, s := range sessions {
		marker := ""
		if s.Name == active {
			marker = "  ← active"
		}
		sb.WriteString(fmt.Sprintf("  • %s%s\n", s.Name, marker))
	}
	if active != "" {
		sb.WriteString(fmt.Sprintf("\nActive session: %s (targeted when no session name is given).", active))
	}
	return sb.String(), nil
}

func (t *tmuxTools) setActiveSession(ctx context.Context, a map[string]interface{}) (string, error) {
	name := argString(a, "name")
	if name == "" {
		return "", fmt.Errorf("missing required argument: name")
	}
	exists, err := t.mgr.SessionExists(name)
	if err != nil {
		return "", fmt.Errorf("checking session existence: %w", err)
	}
	if !exists {
		return "", fmt.Errorf("tmux session '%s' does not exist", name)
	}
	t.setActive(name)
	return fmt.Sprintf("Active tmux session set to '%s'. Commands without an explicit session name now target it.", name), nil
}

func (t *tmuxTools) createSession(ctx context.Context, a map[string]interface{}) (string, error) {
	name := argString(a, "name")
	if name == "" {
		return "", fmt.Errorf("missing required argument: name")
	}
	dir := argString(a, "directory")
	if dir == "" {
		dir = "."
	}
	command := argString(a, "command")

	sess, err := t.mgr.CreateSession(name, dir, command)
	if err != nil {
		return "", fmt.Errorf("creating tmux session: %w", err)
	}
	t.setActive(sess.Name)

	content := fmt.Sprintf("Created tmux session '%s' (now the active session).", sess.Name)
	if command != "" {
		content += fmt.Sprintf("\nCommand: %s", command)
	}
	return content, nil
}

func (t *tmuxTools) killSession(ctx context.Context, a map[string]interface{}) (string, error) {
	name := argString(a, "name")
	if name == "" {
		return "", fmt.Errorf("missing required argument: name")
	}
	if err := t.mgr.KillSession(name); err != nil {
		return "", fmt.Errorf("killing tmux session: %w", err)
	}
	t.clearActiveIf(name)
	return fmt.Sprintf("Killed tmux session '%s'.", name), nil
}

func (t *tmuxTools) sendKeys(ctx context.Context, a map[string]interface{}) (string, error) {
	name, err := t.resolveName(a)
	if err != nil {
		return "", err
	}
	keys := argString(a, "keys")
	if keys == "" {
		return "", fmt.Errorf("missing required argument: keys")
	}
	enter := argBool(a, "enter", true)
	if err := t.mgr.SendKeys(name, keys, enter); err != nil {
		return "", fmt.Errorf("sending keys to tmux session: %w", err)
	}
	return fmt.Sprintf("Sent keys to tmux session '%s': %s", name, keys), nil
}

func (t *tmuxTools) capturePane(ctx context.Context, a map[string]interface{}) (string, error) {
	name, err := t.resolveName(a)
	if err != nil {
		return "", err
	}
	output, err := t.mgr.CapturePane(name, argInt(a, "lines", 0))
	if err != nil {
		return "", fmt.Errorf("capturing pane: %w", err)
	}
	return tmuxTruncate(output, 4000), nil
}

func (t *tmuxTools) runCommand(ctx context.Context, a map[string]interface{}) (string, error) {
	name, err := t.resolveName(a)
	if err != nil {
		return "", err
	}
	command := argString(a, "command")
	if command == "" {
		return "", fmt.Errorf("missing required argument: command")
	}
	enter := argBool(a, "enter", true)
	timeoutSec := argInt(a, "timeout_seconds", 0)

	output, err := t.mgr.RunCommand(name, command, enter, timeoutSec)
	if err != nil {
		return "", fmt.Errorf("running command in tmux session: %w", err)
	}
	return tmuxTruncate(output, 4000), nil
}

func (t *tmuxTools) renameSession(ctx context.Context, a map[string]interface{}) (string, error) {
	oldName := argString(a, "old_name")
	if oldName == "" {
		return "", fmt.Errorf("missing required argument: old_name")
	}
	newName := argString(a, "new_name")
	if newName == "" {
		return "", fmt.Errorf("missing required argument: new_name")
	}
	if err := t.mgr.RenameSession(oldName, newName); err != nil {
		return "", fmt.Errorf("renaming tmux session: %w", err)
	}
	if t.getActive() == oldName {
		t.setActive(newName)
	}
	return fmt.Sprintf("Renamed tmux session '%s' → '%s'.", oldName, newName), nil
}

func (t *tmuxTools) suspendSession(ctx context.Context, a map[string]interface{}) (string, error) {
	name := argString(a, "name")
	if name == "" {
		return "", fmt.Errorf("missing required argument: name")
	}
	if err := t.mgr.SuspendSession(name); err != nil {
		return "", fmt.Errorf("suspending tmux session: %w", err)
	}
	return fmt.Sprintf("Suspended tmux session '%s'.", name), nil
}

func (t *tmuxTools) resumeSession(ctx context.Context, a map[string]interface{}) (string, error) {
	name := argString(a, "name")
	if name == "" {
		return "", fmt.Errorf("missing required argument: name")
	}
	command := argString(a, "command")
	if err := t.mgr.ResumeSession(name, command); err != nil {
		return "", fmt.Errorf("resuming tmux session: %w", err)
	}
	content := fmt.Sprintf("Resumed tmux session '%s'.", name)
	if command != "" {
		content += fmt.Sprintf("\nCommand: %s", command)
	}
	return content, nil
}
