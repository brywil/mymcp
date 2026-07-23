package tools

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"
)

// gitTools wraps the git CLI, scoped to the workspace.
type gitTools struct {
	root    string
	timeout time.Duration
}

func (g *gitTools) register(r *Registry) {
	r.Register(&Tool{Name: "git_status", Description: "git status --short --branch.", ReadOnly: true,
		Handler: g.simple("status", "--short", "--branch")})
	r.Register(&Tool{Name: "git_diff", Description: "git diff (optionally staged).",
		Schema:   obj(map[string]interface{}{"staged": map[string]interface{}{"type": "boolean", "description": "Show staged diff"}}),
		ReadOnly: true, Handler: g.diff})
	r.Register(&Tool{Name: "git_log", Description: "git log --oneline -n <n> (default 20).",
		Schema:   obj(map[string]interface{}{"n": map[string]interface{}{"type": "integer", "description": "Number of commits"}}),
		ReadOnly: true, Handler: g.log})
	r.Register(&Tool{Name: "git_commit", Description: "git commit -am <message>.",
		Schema:  obj(map[string]interface{}{"message": strProp("Commit message")}, "message"),
		Handler: g.commit})
	r.Register(&Tool{Name: "git_push", Description: "git push.", Handler: g.simple("push")})
}

func (g *gitTools) exec(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", g.root}, args...)...)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	out := buf.String()
	if err != nil {
		return out, fmt.Errorf("git %v: %v", args, err)
	}
	if out == "" {
		out = "(ok)"
	}
	return out, nil
}

func (g *gitTools) simple(args ...string) Handler {
	return func(ctx context.Context, _ map[string]interface{}) (string, error) { return g.exec(ctx, args...) }
}

func (g *gitTools) diff(ctx context.Context, a map[string]interface{}) (string, error) {
	if argBool(a, "staged", false) {
		return g.exec(ctx, "diff", "--staged")
	}
	return g.exec(ctx, "diff")
}

func (g *gitTools) log(ctx context.Context, a map[string]interface{}) (string, error) {
	n := argInt(a, "n", 20)
	return g.exec(ctx, "log", "--oneline", fmt.Sprintf("-n%d", n))
}

func (g *gitTools) commit(ctx context.Context, a map[string]interface{}) (string, error) {
	msg := argString(a, "message")
	if msg == "" {
		return "", fmt.Errorf("message is required")
	}
	return g.exec(ctx, "commit", "-am", msg)
}
