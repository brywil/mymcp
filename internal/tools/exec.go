package tools

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"
)

// execTools provides shell command execution within the workspace.
type execTools struct {
	root    string
	timeout time.Duration
}

func (x *execTools) register(r *Registry) {
	r.Register(&Tool{
		Name:        "run_command",
		Description: "Run a shell command (sh -c) in the workspace and return combined stdout+stderr.",
		Schema: obj(map[string]interface{}{
			"command": strProp("Shell command line"),
			"timeout_sec": map[string]interface{}{
				"type": "integer", "description": "Optional timeout in seconds",
			},
		}, "command"),
		ReadOnly: false,
		Handler:  x.run,
	})
}

func (x *execTools) run(ctx context.Context, a map[string]interface{}) (string, error) {
	command := argString(a, "command")
	if command == "" {
		return "", fmt.Errorf("command is required")
	}
	timeout := x.timeout
	if t := argInt(a, "timeout_sec", 0); t > 0 {
		timeout = time.Duration(t) * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = x.root
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	out := buf.String()
	if ctx.Err() == context.DeadlineExceeded {
		return out, fmt.Errorf("command timed out after %s", timeout)
	}
	if err != nil {
		return out, fmt.Errorf("exit: %v", err)
	}
	if out == "" {
		out = "(no output)"
	}
	return out, nil
}
