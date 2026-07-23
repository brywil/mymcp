package policy_test

import (
	"testing"

	"github.com/brywil/mymcp/internal/policy"
)

func TestDenyByDefaultAndGrants(t *testing.T) {
	e, err := policy.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	// Unknown principal: not known, nothing allowed.
	if e.Known("alice") {
		t.Fatal("alice should be unknown before any grant")
	}
	if e.Allow("alice", "read_file", true) {
		t.Fatal("unknown principal must be denied")
	}

	// ro grants read-only tools only.
	if _, err := e.Grant("alice", "ro"); err != nil {
		t.Fatal(err)
	}
	if !e.Known("alice") {
		t.Fatal("alice should be known after a grant")
	}
	if !e.Allow("alice", "read_file", true) {
		t.Fatal("ro should permit a read-only tool")
	}
	if e.Allow("alice", "run_command", false) {
		t.Fatal("ro must NOT permit a non-read-only tool")
	}

	// all grants everything.
	if _, err := e.Grant("bob", "all"); err != nil {
		t.Fatal(err)
	}
	if !e.Allow("bob", "run_command", false) {
		t.Fatal("all should permit any tool")
	}

	// a specific tool grant permits only that tool.
	if _, err := e.Grant("carol", "git_status"); err != nil {
		t.Fatal(err)
	}
	if !e.Allow("carol", "git_status", true) {
		t.Fatal("explicit tool grant should permit that tool")
	}
	if e.Allow("carol", "run_command", false) {
		t.Fatal("explicit tool grant must not permit other tools")
	}
}

func TestGrantPersistsAndRemove(t *testing.T) {
	dir := t.TempDir()
	e, _ := policy.Load(dir)
	if _, err := e.Grant("dave", "read_file"); err != nil {
		t.Fatal(err)
	}
	// A fresh Enforcer over the same dir sees the grant (it's a file).
	e2, _ := policy.Load(dir)
	if !e2.Allow("dave", "read_file", true) {
		t.Fatal("grant did not persist to disk")
	}
	if err := e2.Remove("dave"); err != nil {
		t.Fatal(err)
	}
	if e2.Known("dave") {
		t.Fatal("dave should be gone after Remove")
	}
}

func TestListSorted(t *testing.T) {
	e, _ := policy.Load(t.TempDir())
	e.Grant("zeb", "ro")
	e.Grant("amy", "all")
	got := e.List()
	if len(got) != 2 || got[0].CN != "amy" || got[1].CN != "zeb" {
		t.Fatalf("List should be sorted by CN: %+v", got)
	}
}
