package tools

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func waitArgs(kv ...interface{}) map[string]interface{} {
	m := map[string]interface{}{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	return m
}

// A condition already true must return immediately — the common case is "check
// whether the thing I need has already happened", and paying a poll interval
// for that would make the tool worse than an inline check.
func TestWaitForReturnsImmediatelyWhenAlreadyTrue(t *testing.T) {
	m := &miscTools{}
	start := time.Now()
	out, err := m.waitFor(context.Background(), waitArgs(
		"condition", "command_succeeds", "command", "true", "timeout_seconds", 30))
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("took %s for an already-true condition", elapsed)
	}
	if !strings.Contains(out, "Condition met") {
		t.Errorf("unexpected result: %s", out)
	}
}

// A timeout must SAY it timed out, in terms a model cannot mistake for success.
// This is the property that matters most: a wait that quietly gives up and
// returns something cheerful is how "the download finished" becomes a lie.
func TestWaitForTimeoutIsUnmistakable(t *testing.T) {
	m := &miscTools{}
	out, err := m.waitFor(context.Background(), waitArgs(
		"condition", "command_succeeds", "command", "false",
		"timeout_seconds", 1, "poll_seconds", 1))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "TIMED OUT") {
		t.Errorf("timeout not clearly reported: %s", out)
	}
	if !strings.Contains(out, "do not assume it succeeded") {
		t.Errorf("timeout does not warn against assuming success: %s", out)
	}
	if strings.Contains(out, "Condition met") {
		t.Errorf("a timeout claimed success: %s", out)
	}
}

// process_exit must refuse a name, and the refusal must explain why — this is
// the pgrep self-match trap encoded as a guardrail.
func TestProcessExitRefusesPatterns(t *testing.T) {
	_, _, err := buildWaitCondition("process_exit", waitArgs("pid", 0))
	if err == nil {
		t.Fatal("expected a missing pid to be refused")
	}
	if !strings.Contains(err.Error(), "caller's own command line") {
		t.Errorf("refusal should explain the pattern-matching trap, got: %v", err)
	}
}

func TestWaitForProcessExit(t *testing.T) {
	cmd := exec.Command("sleep", "1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	go func() { _ = cmd.Wait() }()

	m := &miscTools{}
	out, err := m.waitFor(context.Background(), waitArgs(
		"condition", "process_exit", "pid", pid, "timeout_seconds", 20, "poll_seconds", 1))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Condition met") || !strings.Contains(out, "has exited") {
		t.Errorf("did not observe the exit: %s", out)
	}
}

// A live PID must NOT be reported as exited.
func TestProcessAliveDetectsSelf(t *testing.T) {
	if !processAlive(os.Getpid()) {
		t.Error("the test process reported itself as dead")
	}
	// PID 1 exists but is not ours: EPERM must count as alive, not as gone.
	if !processAlive(1) {
		t.Error("pid 1 reported as gone — EPERM is being treated as absence")
	}
}

func TestWaitForPortListeningAndGone(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	m := &miscTools{}
	out, err := m.waitFor(context.Background(), waitArgs(
		"condition", "port_listening", "port", port, "timeout_seconds", 5, "poll_seconds", 1))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Condition met") {
		t.Errorf("open port not detected: %s", out)
	}

	ln.Close()
	out, err = m.waitFor(context.Background(), waitArgs(
		"condition", "port_listening", "port", port, "gone", true,
		"timeout_seconds", 5, "poll_seconds", 1))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Condition met") {
		t.Errorf("closed port not detected: %s", out)
	}
	// The whole reason this box got wedged: a freed port is not freed memory.
	if !strings.Contains(out, "does NOT mean freed memory") {
		t.Errorf("port-gone result must warn that memory lags the socket: %s", out)
	}
}

func TestWaitForHTTPOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	m := &miscTools{}
	out, err := m.waitFor(context.Background(), waitArgs(
		"condition", "http_ok", "url", srv.URL, "timeout_seconds", 5, "poll_seconds", 1))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "HTTP 200") {
		t.Errorf("2xx not reported: %s", out)
	}
}

// A server that answers 503 is not ready; the wait must not treat any response
// as success.
func TestWaitForHTTPRejectsNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	m := &miscTools{}
	out, _ := m.waitFor(context.Background(), waitArgs(
		"condition", "http_ok", "url", srv.URL, "timeout_seconds", 1, "poll_seconds", 1))
	if !strings.Contains(out, "TIMED OUT") {
		t.Errorf("503 was accepted as ready: %s", out)
	}
}

func TestWaitForFileStable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "download.bin")
	if err := os.WriteFile(path, []byte("partial"), 0644); err != nil {
		t.Fatal(err)
	}
	// Grow it once, then leave it alone.
	go func() {
		time.Sleep(1200 * time.Millisecond)
		f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
		if f != nil {
			_, _ = f.WriteString("more")
			_ = f.Close()
		}
	}()

	m := &miscTools{}
	out, err := m.waitFor(context.Background(), waitArgs(
		"condition", "file_stable", "path", path,
		"quiet_seconds", 2, "timeout_seconds", 20, "poll_seconds", 1))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Condition met") || !strings.Contains(out, "unchanged") {
		t.Errorf("stability not detected: %s", out)
	}
}

// A file that never appears is a timeout, not a crash.
func TestWaitForFileStableMissingFile(t *testing.T) {
	m := &miscTools{}
	out, err := m.waitFor(context.Background(), waitArgs(
		"condition", "file_stable", "path", filepath.Join(t.TempDir(), "nope"),
		"timeout_seconds", 1, "poll_seconds", 1))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "TIMED OUT") || !strings.Contains(out, "does not exist") {
		t.Errorf("missing file not reported clearly: %s", out)
	}
}

func TestMemAvailableIsPlausible(t *testing.T) {
	mb, err := memAvailableMB()
	if err != nil {
		t.Skipf("no /proc/meminfo: %v", err)
	}
	if mb <= 0 || mb > 4_000_000 {
		t.Errorf("MemAvailable = %d MB, implausible", mb)
	}
}

// Arguments are validated BEFORE waiting, so a malformed call fails in
// milliseconds rather than after the full timeout.
func TestBadArgumentsFailFast(t *testing.T) {
	m := &miscTools{}
	for _, args := range []map[string]interface{}{
		waitArgs("condition", "memory_free"),      // no mb
		waitArgs("condition", "port_listening"),   // no port
		waitArgs("condition", "http_ok"),          // no url
		waitArgs("condition", "file_stable"),      // no path
		waitArgs("condition", "command_succeeds"), // no command
		waitArgs("condition", "nonsense"),         // unknown
	} {
		start := time.Now()
		if _, err := m.waitFor(context.Background(), args); err == nil {
			t.Errorf("expected an error for %v", args)
		}
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Errorf("validation for %v took %s; it must fail before waiting", args, elapsed)
		}
	}
}

// Cancellation must be honoured promptly — a 3600-second wait that ignores ctx
// would pin a tool slot for an hour.
func TestWaitForHonoursCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(300 * time.Millisecond); cancel() }()

	m := &miscTools{}
	start := time.Now()
	out, err := m.waitFor(ctx, waitArgs(
		"condition", "command_succeeds", "command", "false",
		"timeout_seconds", 3600, "poll_seconds", 1))
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("cancellation ignored: waited %s", elapsed)
	}
	if !strings.Contains(out, "Cancelled") {
		t.Errorf("cancellation not reported: %s", out)
	}
}

// The timeout is clamped, so a caller cannot block forever by asking for a year.
// Asserted on the constant rather than by calling waitFor with a huge timeout —
// the first draft of this test did exactly that against a command that never
// succeeds, which would have blocked the suite for the full clamped hour.
func TestTimeoutIsClamped(t *testing.T) {
	if maxWaitTimeout > 3600 {
		t.Errorf("max timeout %d is too generous for a blocking tool", maxWaitTimeout)
	}
	if defaultWaitTimeout <= 0 || defaultWaitTimeout > maxWaitTimeout {
		t.Errorf("default timeout %d is not within (0, %d]", defaultWaitTimeout, maxWaitTimeout)
	}
}
