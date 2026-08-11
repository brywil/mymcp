package tools

import (
	"os/exec"
	"strings"
	"testing"
)

// These exercise real tmux on this host, because the failure being fixed was a
// real-tmux failure: `-c ""` made new-session fail with "can't find directory",
// and resolveName's new auto-create path depends on it working. A fake manager
// would have happily returned success.
//
// Skipped when tmux is unavailable, and every session created is named with a
// test-only prefix and killed on the way out.
const testSessionPrefix = "mymcp-test-"

func tmuxAvailable(t *testing.T) *tmuxManager {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	return &tmuxManager{}
}

func TestCreateSessionWithoutDir(t *testing.T) {
	m := tmuxAvailable(t)
	name := testSessionPrefix + "nodir"
	_ = m.KillSession(name) // in case a previous run died mid-test
	if _, err := m.CreateSession(name, "", ""); err != nil {
		t.Fatalf("creating a session with no dir failed (this is the `-c \"\"` bug): %v", err)
	}
	defer m.KillSession(name)

	exists, err := m.SessionExists(name)
	if err != nil || !exists {
		t.Errorf("session not found after creation: exists=%v err=%v", exists, err)
	}
}

// The whole point of the change: a caller that names no session gets a working
// one instead of an error telling it to go set one up.
func TestResolveNameCreatesDefaultWhenNoneExist(t *testing.T) {
	m := tmuxAvailable(t)
	sessions, err := m.ListSessions()
	if err != nil {
		t.Skipf("cannot list sessions: %v", err)
	}
	if len(sessions) > 0 {
		// This host has real sessions (the goclaw one, usually). Assert the
		// unambiguous-adoption rule instead of destroying them to test creation.
		tt := &tmuxTools{mgr: m}
		got, err := tt.resolveName(map[string]interface{}{})
		if len(sessions) == 1 {
			if err != nil || got != sessions[0].Name {
				t.Errorf("with exactly one session, resolveName should adopt %q; got %q err=%v",
					sessions[0].Name, got, err)
			}
			return
		}
		// Several sessions: it must refuse rather than guess, and the message
		// must name them and the tool that fixes it.
		if err == nil {
			t.Errorf("with %d sessions, resolveName should refuse to guess; got %q", len(sessions), got)
			return
		}
		if !strings.Contains(err.Error(), "set_active_session") {
			t.Errorf("refusal should name the fixing tool, got: %v", err)
		}
		return
	}

	tt := &tmuxTools{mgr: m}
	got, err := tt.resolveName(map[string]interface{}{})
	if err != nil {
		t.Fatalf("resolveName should have created a session, got: %v", err)
	}
	defer m.KillSession(got)
	if got != defaultSessionName {
		t.Errorf("created session = %q, want %q", got, defaultSessionName)
	}
}

// An explicit name always wins and becomes active.
func TestResolveNameExplicitWins(t *testing.T) {
	tt := &tmuxTools{mgr: &tmuxManager{}}
	got, err := tt.resolveName(map[string]interface{}{"name": "explicit"})
	if err != nil || got != "explicit" {
		t.Fatalf("resolveName(name=explicit) = %q, %v", got, err)
	}
	if tt.getActive() != "explicit" {
		t.Errorf("explicit name did not become active: %q", tt.getActive())
	}
}
