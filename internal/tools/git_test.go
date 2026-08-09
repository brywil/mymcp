package tools

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// newGitRepo creates a throwaway repo with one commit.
func newGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(cmd.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git unavailable or failed (%v): %s", err, out)
		}
	}
	run("init", "-q")
	if err := writeFile(dir+"/README.md", "hello\n"); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-qm", "initial commit")
	return dir
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0644)
}

func TestGitQueryReads(t *testing.T) {
	dir := newGitRepo(t)
	g := &gitTools{root: dir}

	out, err := g.query(context.Background(), map[string]interface{}{
		"repo_path":  dir,
		"subcommand": "log",
		"args":       []interface{}{"--oneline"},
	})
	if err != nil {
		t.Fatalf("git log failed: %v", err)
	}
	if !strings.Contains(out, "initial commit") {
		t.Errorf("expected the commit subject in output, got %q", out)
	}
}

// The allowlist is the whole security boundary — if a mutating verb slips
// through, the tool is not read-only regardless of what its description says.
func TestGitQueryRefusesMutatingVerbs(t *testing.T) {
	dir := newGitRepo(t)
	g := &gitTools{root: dir}

	for _, sub := range []string{"commit", "push", "checkout", "merge", "pull", "clean", "stash", "reset", "config", "fetch", "submodule", "switch", "restore"} {
		_, err := g.query(context.Background(), map[string]interface{}{
			"repo_path":  dir,
			"subcommand": sub,
		})
		if err == nil {
			t.Errorf("git %s was permitted — it must be refused", sub)
			continue
		}
		if !strings.Contains(err.Error(), "read-only") {
			t.Errorf("git %s refused with an unhelpful error: %v", sub, err)
		}
	}
}

// Verbs whose bare form lists but which mutate with a flag.
func TestGitQueryRefusesMutatingFlags(t *testing.T) {
	dir := newGitRepo(t)
	g := &gitTools{root: dir}

	cases := []struct{ sub, arg string }{
		{"tag", "-d"},
		{"branch", "-D"},
		{"branch", "-m"},
		{"remote", "add"},
		{"remote", "set-url"},
	}
	for _, c := range cases {
		_, err := g.query(context.Background(), map[string]interface{}{
			"repo_path":  dir,
			"subcommand": c.sub,
			"args":       []interface{}{c.arg},
		})
		if err == nil {
			t.Errorf("git %s %s was permitted — it mutates", c.sub, c.arg)
		}
	}
}

// -c and --exec let git run something other than the subcommand requested.
func TestGitQueryRefusesCommandInjectionFlags(t *testing.T) {
	dir := newGitRepo(t)
	g := &gitTools{root: dir}

	for _, arg := range []string{"-c", "--exec=touch /tmp/pwned", "--git-dir=/etc", "--work-tree=/"} {
		_, err := g.query(context.Background(), map[string]interface{}{
			"repo_path":  dir,
			"subcommand": "log",
			"args":       []interface{}{arg},
		})
		if err == nil {
			t.Errorf("argument %q was permitted — it can retarget or run other commands", arg)
		}
	}
}

func TestGitQueryRequiresRepoPath(t *testing.T) {
	g := &gitTools{root: ""}
	if _, err := g.query(context.Background(), map[string]interface{}{"subcommand": "log"}); err == nil {
		t.Error("expected an error when repo_path is missing")
	}
}
