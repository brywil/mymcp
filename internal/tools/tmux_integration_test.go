package tools

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// End-to-end against a real tmux server. The unit tests exercise the parser on
// synthetic panes; this exercises the part that actually broke — the send
// sequence — because the old bug only appeared with a command slow enough that
// the sentinel got typed while it was still running.
func TestRunCommand_RealTmux_SlowCommandOutputIntact(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	sock := fmt.Sprintf("/tmp/mymcp-test-%d", time.Now().UnixNano())
	m := &tmuxManager{SocketPath: sock}
	name := "itest"

	if _, err := m.CreateSession(name, "/tmp", ""); err != nil {
		t.Fatalf("create session: %v", err)
	}
	defer exec.Command("tmux", "-S", sock, "kill-server").Run()

	// 3 seconds is comfortably longer than the old code's window between Enter
	// and typing the sentinel. Under the old implementation the sentinel was
	// echoed into the middle of this command's output and everything after it
	// was discarded, so BOTTOM_LINE never came back.
	out, err := m.RunCommand(name, "echo TOP_LINE; sleep 3; echo BOTTOM_LINE", true, 30)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	t.Logf("captured:\n%s", out)

	if !strings.Contains(out, "TOP_LINE") {
		t.Errorf("start of output missing:\n%s", out)
	}
	if !strings.Contains(out, "BOTTOM_LINE") {
		t.Errorf("output truncated at the sentinel — the original bug:\n%s", out)
	}
	if strings.Contains(out, "__MYMCP_") {
		t.Errorf("marker leaked into output:\n%s", out)
	}
	if !strings.Contains(out, "exit code 0") {
		t.Errorf("exit code missing:\n%s", out)
	}
}

// Two commands in a row in the same session: the second must not return the
// first's output. This is the "pane echoes the whole history" symptom.
func TestRunCommand_RealTmux_NoBleedBetweenCalls(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	sock := fmt.Sprintf("/tmp/mymcp-test-%d", time.Now().UnixNano())
	m := &tmuxManager{SocketPath: sock}
	name := "itest2"

	if _, err := m.CreateSession(name, "/tmp", ""); err != nil {
		t.Fatalf("create session: %v", err)
	}
	defer exec.Command("tmux", "-S", sock, "kill-server").Run()

	if _, err := m.RunCommand(name, "echo FIRST_COMMAND_OUTPUT", true, 20); err != nil {
		t.Fatalf("first run: %v", err)
	}
	out, err := m.RunCommand(name, "echo SECOND_COMMAND_OUTPUT", true, 20)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	t.Logf("second call returned:\n%s", out)

	if strings.Contains(out, "FIRST_COMMAND_OUTPUT") {
		t.Errorf("previous command's output bled into this one:\n%s", out)
	}
	if !strings.Contains(out, "SECOND_COMMAND_OUTPUT") {
		t.Errorf("current output missing:\n%s", out)
	}
}

// A non-zero exit must be reported rather than swallowed, so a failing command
// is distinguishable from one that printed nothing.
func TestRunCommand_RealTmux_NonZeroExit(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	sock := fmt.Sprintf("/tmp/mymcp-test-%d", time.Now().UnixNano())
	m := &tmuxManager{SocketPath: sock}
	name := "itest3"

	if _, err := m.CreateSession(name, "/tmp", ""); err != nil {
		t.Fatalf("create session: %v", err)
	}
	defer exec.Command("tmux", "-S", sock, "kill-server").Run()

	out, err := m.RunCommand(name, "ls /definitely-not-here", true, 20)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if strings.Contains(out, "exit code 0") {
		t.Errorf("failing command reported success:\n%s", out)
	}
}
