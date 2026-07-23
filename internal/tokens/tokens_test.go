package tokens_test

import (
	"testing"

	"github.com/brywil/mymcp/internal/tokens"
)

func TestMultipleTokensAuthenticateByName(t *testing.T) {
	s, err := tokens.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	alice, err := s.Add("alice")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := s.Add("bob")
	if err != nil {
		t.Fatal(err)
	}
	if alice == bob {
		t.Fatal("tokens must be distinct")
	}
	if name, ok := s.Authenticate(alice); !ok || name != "alice" {
		t.Fatalf("alice token -> %q, %v", name, ok)
	}
	if name, ok := s.Authenticate(bob); !ok || name != "bob" {
		t.Fatalf("bob token -> %q, %v", name, ok)
	}
	if _, ok := s.Authenticate("nonsense"); ok {
		t.Fatal("unknown token must not authenticate")
	}
	if _, ok := s.Authenticate(""); ok {
		t.Fatal("empty token must not authenticate")
	}
}

func TestRevocation(t *testing.T) {
	s, _ := tokens.Load(t.TempDir())
	tok, _ := s.Add("agent")
	if _, ok := s.Authenticate(tok); !ok {
		t.Fatal("token should authenticate before revoke")
	}
	if err := s.Revoke("agent"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Authenticate(tok); ok {
		t.Fatal("token must NOT authenticate after revoke")
	}
	if err := s.Revoke("agent"); err == nil {
		t.Fatal("revoking a missing token should error")
	}
}

func TestAddDuplicateAndInvalidNames(t *testing.T) {
	s, _ := tokens.Load(t.TempDir())
	if _, err := s.Add("dup"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add("dup"); err == nil {
		t.Fatal("adding an existing name should error")
	}
	for _, bad := range []string{"", "..", "a/b", "x\\y", "with space"} {
		if _, err := s.Add(bad); err == nil {
			t.Errorf("Add(%q) should be rejected", bad)
		}
	}
}

func TestEnsureDefaultAndList(t *testing.T) {
	s, _ := tokens.Load(t.TempDir())
	created, err := s.EnsureDefault()
	if err != nil || !created {
		t.Fatalf("EnsureDefault should create on empty store: created=%v err=%v", created, err)
	}
	if created, _ := s.EnsureDefault(); created {
		t.Fatal("EnsureDefault should be a no-op when a token exists")
	}
	s.Add("zzz")
	names, _ := s.List()
	if len(names) != 2 || names[0] != "default" || names[1] != "zzz" {
		t.Fatalf("List = %v, want [default zzz]", names)
	}
}
