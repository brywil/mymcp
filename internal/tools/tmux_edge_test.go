package tools

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// --- edge cases in how the command line is composed ---
//
// The command, START marker and DONE marker are sent as ONE shell line so that
// nothing is typed while the command runs. That makes the command's own text
// part of the line, so it must not be able to terminate it -- hence eval with
// the command single-quoted. Without that, `ls # note` comments out the DONE
// marker and the tool waits out the whole timeout on a command that already
// finished.

func TestRunCommand_TrailingComment(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("no tmux")
	}
	sock := fmt.Sprintf("/tmp/mymcp-edge-%d", time.Now().UnixNano())
	m := &tmuxManager{SocketPath: sock}
	if _, err := m.CreateSession("edge", "/tmp", ""); err != nil {
		t.Fatal(err)
	}
	defer exec.Command("tmux", "-S", sock, "kill-server").Run()
	out, err := m.RunCommand("edge", "echo HELLO # a trailing comment", true, 8)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("result:\n%s", out)
	if strings.Contains(out, "still running") {
		t.Errorf("trailing comment swallowed the marker -> hung until timeout")
	}
}

func TestRunCommand_QuotesAndSpecials(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("no tmux")
	}
	sock := fmt.Sprintf("/tmp/mymcp-edge2-%d", time.Now().UnixNano())
	m := &tmuxManager{SocketPath: sock}
	if _, err := m.CreateSession("edge2", "/tmp", ""); err != nil {
		t.Fatal(err)
	}
	defer exec.Command("tmux", "-S", sock, "kill-server").Run()
	for _, tc := range []struct{ name, cmd, want string }{
		{"single quotes", `echo 'hello world'`, "hello world"},
		{"double quotes", `echo "quoted"`, "quoted"},
		{"pipe", `echo abc | tr a-z A-Z`, "ABC"},
		{"semicolons", `echo one; echo two`, "two"},
		{"dollar var", `X=5; echo "val=$X"`, "val=5"},
		{"nested quote", `echo "it's fine"`, "it's fine"},
	} {
		out, err := m.RunCommand("edge2", tc.cmd, true, 15)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if !strings.Contains(out, tc.want) {
			t.Errorf("%s: want %q in:\n%s", tc.name, tc.want, out)
		}
		if strings.Contains(out, "still running") {
			t.Errorf("%s: hung", tc.name)
		}
	}
}
