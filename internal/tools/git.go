package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// gitTools wraps the git CLI (plus ssh/ssh-keygen for ssh_* tools), scoped to
// the workspace. Ported from goclaw's GitTool for name+schema parity.
type gitTools struct {
	root    string
	timeout time.Duration
}

// maxGitOutput limits output to prevent context overflow.
const maxGitOutput = 8000

// sshKeyPath is the SSH private key used for git operations.
func (g *gitTools) sshKeyPath() string {
	return filepath.Join(g.root, ".ssh", "agent_key")
}

func (g *gitTools) register(r *Registry) {
	r.Register(&Tool{Name: "git_status", Description: "Show the working tree status.", ReadOnly: true,
		Schema:  gitStatusSchema(),
		Handler: g.status})
	r.Register(&Tool{Name: "git_diff", Description: "Show changes between commits, the working tree, etc.", ReadOnly: true,
		Schema:  gitDiffSchema(),
		Handler: g.diff})
	r.Register(&Tool{Name: "git_log", Description: "Show commit history.", ReadOnly: true,
		Schema:  gitLogSchema(),
		Handler: g.log})
	r.Register(&Tool{Name: "git_show", Description: "Show details of a single commit or object.", ReadOnly: true,
		Schema:  gitShowSchema(),
		Handler: g.show})
	r.Register(&Tool{Name: "git_branch", Description: "List branches.", ReadOnly: true,
		Schema:  gitBranchSchema(),
		Handler: g.branch})
	r.Register(&Tool{Name: "git_add", Description: "Stage files.",
		Schema:  gitAddSchema(),
		Handler: g.add})
	r.Register(&Tool{Name: "git_commit", Description: "Create a new commit.",
		Schema:  gitCommitSchema(),
		Handler: g.commit})
	r.Register(&Tool{Name: "git_push", Description: "Push to a remote.",
		Schema:  gitPushSchema(),
		Handler: g.push})
	r.Register(&Tool{Name: "git_pull", Description: "Pull from a remote.",
		Schema:  gitPullSchema(),
		Handler: g.pull})
	r.Register(&Tool{Name: "git_remote", Description: "Show configured remotes.", ReadOnly: true,
		Schema:  gitRemoteSchema(),
		Handler: g.remote})
	r.Register(&Tool{Name: "git_rev_parse", Description: "Resolve a ref to a commit hash.", ReadOnly: true,
		Schema:  gitRevParseSchema(),
		Handler: g.revParse})
	r.Register(&Tool{Name: "git_merge", Description: "Create a merge commit.",
		Schema:  gitMergeSchema(),
		Handler: g.merge})
	r.Register(&Tool{Name: "git_checkout", Description: "Switch branches or restore files.",
		Schema:  gitCheckoutSchema(),
		Handler: g.checkout})
	r.Register(&Tool{Name: "git_clone", Description: "Clone a repository into the workspace.",
		Schema:  gitCloneSchema(),
		Handler: g.clone})
	r.Register(&Tool{Name: "git_ssh_key", Description: "Display the SSH key used for git operations.",
		Schema:  gitEmptySchema(),
		Handler: g.sshKey})
	r.Register(&Tool{Name: "git_ssh_key_generate", Description: "Generate a new SSH key pair and configure ssh to use it.",
		Schema:  gitSSHKeyGenerateSchema(),
		Handler: g.sshKeyGenerate})
	r.Register(&Tool{Name: "git_ssh_verify", Description: "Verify SSH authentication with GitHub.", ReadOnly: true,
		Schema:  gitEmptySchema(),
		Handler: g.sshVerify})
	r.Register(&Tool{Name: "git_ssh_key_list", Description: "List SSH keys available locally.", ReadOnly: true,
		Schema:  gitEmptySchema(),
		Handler: g.sshKeyList})
}

// gitRun executes a git command in repo and returns truncated output.
func (g *gitTools) gitRun(ctx context.Context, repo string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), fmt.Sprintf("GIT_SSH_COMMAND=ssh -i %s -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null", g.sshKeyPath()))

	output, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(output))
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git command failed: %s — %s", strings.Join(args, " "), msg)
	}

	result := strings.TrimSpace(string(output))
	if maxGitOutput > 0 && len(result) > maxGitOutput {
		result = result[:maxGitOutput] + "\n\n... (truncated, use more specific query to see more)"
	}
	return result, nil
}

// getRepoPath returns the repo path from args, resolving relative paths against
// the workspace root. Returns an error if not provided.
func (g *gitTools) getRepoPath(a map[string]interface{}) (string, error) {
	repo := argString(a, "repo_path")
	if repo == "" {
		return "", fmt.Errorf("repo_path is required — specify the path to the git repository (e.g., /path/to/repo)")
	}
	if !filepath.IsAbs(repo) && g.root != "" {
		repo = filepath.Join(g.root, repo)
	}
	abs, err := filepath.Abs(repo)
	if err != nil {
		return "", fmt.Errorf("invalid repo_path: %s", repo)
	}
	return abs, nil
}

func (g *gitTools) status(ctx context.Context, a map[string]interface{}) (string, error) {
	repo, err := g.getRepoPath(a)
	if err != nil {
		return "", err
	}
	cmdArgs := []string{"status"}
	if argBool(a, "stat", false) {
		cmdArgs = append(cmdArgs, "--stat")
	} else {
		cmdArgs = append(cmdArgs, "--short")
	}
	return g.gitRun(ctx, repo, cmdArgs...)
}

func (g *gitTools) diff(ctx context.Context, a map[string]interface{}) (string, error) {
	repo, err := g.getRepoPath(a)
	if err != nil {
		return "", err
	}
	var cmdArgs []string
	if path := argString(a, "path"); path != "" {
		cmdArgs = append(cmdArgs, path)
	}
	if argBool(a, "cached", false) {
		cmdArgs = append(cmdArgs, "--cached")
	} else if argBool(a, "staged", false) {
		cmdArgs = append(cmdArgs, "--cached")
	}
	cmdArgs = append([]string{"diff"}, cmdArgs...)
	result, err := g.gitRun(ctx, repo, cmdArgs...)
	if err != nil {
		return "", err
	}
	if result == "" {
		return "No changes detected.", nil
	}
	return result, nil
}

func (g *gitTools) log(ctx context.Context, a map[string]interface{}) (string, error) {
	repo, err := g.getRepoPath(a)
	if err != nil {
		return "", err
	}
	var cmdArgs []string
	max := argInt(a, "max", 20)
	if max <= 0 {
		max = 20
	}
	cmdArgs = append(cmdArgs, fmt.Sprintf("-%d", max))
	if argBool(a, "oneline", false) {
		cmdArgs = append(cmdArgs, "--oneline")
	}
	if path := argString(a, "path"); path != "" {
		cmdArgs = append(cmdArgs, "--", path)
	}
	if branch := argString(a, "branch"); branch != "" {
		cmdArgs = append(cmdArgs, branch)
	}
	cmdArgs = append([]string{"log"}, cmdArgs...)
	return g.gitRun(ctx, repo, cmdArgs...)
}

func (g *gitTools) show(ctx context.Context, a map[string]interface{}) (string, error) {
	repo, err := g.getRepoPath(a)
	if err != nil {
		return "", err
	}
	ref := argString(a, "ref")
	if ref == "" {
		return "", fmt.Errorf("ref is required (e.g., HEAD, a commit hash, branch name)")
	}
	cmdArgs := []string{"show", "--stat", "--pretty=format:commit %H%nAuthor: %an <%ae>%nDate:   %ad%n", ref}
	return g.gitRun(ctx, repo, cmdArgs...)
}

func (g *gitTools) branch(ctx context.Context, a map[string]interface{}) (string, error) {
	repo, err := g.getRepoPath(a)
	if err != nil {
		return "", err
	}
	cmdArgs := []string{"branch"}
	if !argBool(a, "local", false) {
		cmdArgs = append(cmdArgs, "-a")
	}
	return g.gitRun(ctx, repo, cmdArgs...)
}

func (g *gitTools) add(ctx context.Context, a map[string]interface{}) (string, error) {
	repo, err := g.getRepoPath(a)
	if err != nil {
		return "", err
	}
	paths, ok := a["paths"].([]interface{})
	if !ok || len(paths) == 0 {
		return "", fmt.Errorf("paths is required — list of file paths to stage")
	}
	var pathStrings []string
	for _, p := range paths {
		if s, ok := p.(string); ok {
			pathStrings = append(pathStrings, s)
		}
	}
	cmdArgs := append([]string{"add"}, pathStrings...)
	if _, err := g.gitRun(ctx, repo, cmdArgs...); err != nil {
		return "", err
	}
	return fmt.Sprintf("Staged: %s", strings.Join(pathStrings, ", ")), nil
}

func (g *gitTools) commit(ctx context.Context, a map[string]interface{}) (string, error) {
	repo, err := g.getRepoPath(a)
	if err != nil {
		return "", err
	}
	message := argString(a, "message")
	if message == "" {
		return "", fmt.Errorf("message is required — commit message")
	}
	var cmdArgs []string
	if argBool(a, "amend", false) {
		cmdArgs = append(cmdArgs, "--amend")
	}
	if argBool(a, "no_verify", false) {
		cmdArgs = append(cmdArgs, "--no-verify")
	}
	cmdArgs = append(cmdArgs, "-m", message)
	cmdArgs = append([]string{"commit"}, cmdArgs...)
	return g.gitRun(ctx, repo, cmdArgs...)
}

func (g *gitTools) push(ctx context.Context, a map[string]interface{}) (string, error) {
	repo, err := g.getRepoPath(a)
	if err != nil {
		return "", err
	}
	var cmdArgs []string
	if remote := argString(a, "remote"); remote != "" {
		cmdArgs = append(cmdArgs, remote)
	}
	if ref := argString(a, "ref"); ref != "" {
		cmdArgs = append(cmdArgs, ref)
	}
	if argBool(a, "set_upstream", false) {
		cmdArgs = append(cmdArgs, "--set-upstream")
	}
	if argBool(a, "force", false) {
		cmdArgs = append(cmdArgs, "--force")
	}
	cmdArgs = append([]string{"push"}, cmdArgs...)
	return g.gitRun(ctx, repo, cmdArgs...)
}

func (g *gitTools) pull(ctx context.Context, a map[string]interface{}) (string, error) {
	repo, err := g.getRepoPath(a)
	if err != nil {
		return "", err
	}
	var cmdArgs []string
	if remote := argString(a, "remote"); remote != "" {
		cmdArgs = append(cmdArgs, remote)
	}
	if ref := argString(a, "ref"); ref != "" {
		cmdArgs = append(cmdArgs, ref)
	}
	if argBool(a, "rebase", false) {
		cmdArgs = append(cmdArgs, "--rebase")
	}
	cmdArgs = append([]string{"pull"}, cmdArgs...)
	return g.gitRun(ctx, repo, cmdArgs...)
}

func (g *gitTools) remote(ctx context.Context, a map[string]interface{}) (string, error) {
	repo, err := g.getRepoPath(a)
	if err != nil {
		return "", err
	}
	return g.gitRun(ctx, repo, "remote", "-v")
}

func (g *gitTools) revParse(ctx context.Context, a map[string]interface{}) (string, error) {
	repo, err := g.getRepoPath(a)
	if err != nil {
		return "", err
	}
	ref := argString(a, "ref")
	if ref == "" {
		return "", fmt.Errorf("ref is required (e.g., HEAD, branch name)")
	}
	return g.gitRun(ctx, repo, "rev-parse", ref)
}

func (g *gitTools) merge(ctx context.Context, a map[string]interface{}) (string, error) {
	repo, err := g.getRepoPath(a)
	if err != nil {
		return "", err
	}
	branch := argString(a, "branch")
	if branch == "" {
		return "", fmt.Errorf("branch is required — branch name to merge")
	}
	return g.gitRun(ctx, repo, "merge", branch)
}

func (g *gitTools) checkout(ctx context.Context, a map[string]interface{}) (string, error) {
	repo, err := g.getRepoPath(a)
	if err != nil {
		return "", err
	}
	branch := argString(a, "branch")
	if branch == "" {
		return "", fmt.Errorf("branch is required")
	}
	var cmdArgs []string
	if argBool(a, "create", false) {
		cmdArgs = append(cmdArgs, "-b")
	}
	cmdArgs = append(cmdArgs, branch)
	if path := argString(a, "path"); path != "" {
		cmdArgs = append(cmdArgs, "--", path)
	}
	cmdArgs = append([]string{"checkout"}, cmdArgs...)
	return g.gitRun(ctx, repo, cmdArgs...)
}

func (g *gitTools) clone(ctx context.Context, a map[string]interface{}) (string, error) {
	url := argString(a, "url")
	if url == "" {
		return "", fmt.Errorf("url is required")
	}
	dest := g.root
	if dir := argString(a, "directory"); dir != "" {
		dest = filepath.Join(g.root, dir)
	}
	parentDir := filepath.Dir(dest)
	if err := os.MkdirAll(parentDir, 0755); err != nil {
		return "", fmt.Errorf("error creating directory: %s", err.Error())
	}
	cctx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "git", "clone", url, dest)
	cmd.Env = append(os.Environ(), fmt.Sprintf("GIT_SSH_COMMAND=ssh -i %s -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null", g.sshKeyPath()))
	output, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(output))
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s", msg)
	}
	return fmt.Sprintf("Cloned %s into %s", url, dest), nil
}

func (g *gitTools) sshKey(ctx context.Context, _ map[string]interface{}) (string, error) {
	var parts []string

	// 1. Check ssh-add -L (keys loaded in the agent's SSH agent)
	if output, err := exec.CommandContext(ctx, "ssh-add", "-L").CombinedOutput(); err == nil && len(output) > 0 {
		parts = append(parts, "### SSH Agent Keys (ssh-add -L)\n"+strings.TrimSpace(string(output)))
	} else {
		parts = append(parts, "### SSH Agent Keys\nNo keys loaded in SSH agent (ssh-add -L failed or empty).")
	}

	// 2. Check git user config
	nameOut, _ := exec.CommandContext(ctx, "git", "config", "--global", "user.name").CombinedOutput()
	emailOut, _ := exec.CommandContext(ctx, "git", "config", "--global", "user.email").CombinedOutput()
	name := strings.TrimSpace(string(nameOut))
	email := strings.TrimSpace(string(emailOut))
	if name != "" || email != "" {
		parts = append(parts, fmt.Sprintf("### Git Identity\nUser: %s\nEmail: %s", name, email))
	}

	// 3. Check for SSH keys in workspace/.ssh
	sshDir := filepath.Join(g.root, ".ssh")
	if entries, err := os.ReadDir(sshDir); err == nil {
		var pubKeys []string
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".pub") {
				pubKeys = append(pubKeys, e.Name())
			}
		}
		if len(pubKeys) > 0 {
			parts = append(parts, fmt.Sprintf("### SSH Key Files in workspace/.ssh\nPublic keys found: %s", strings.Join(pubKeys, ", ")))
		}
	}

	return strings.Join(parts, "\n\n"), nil
}

func (g *gitTools) sshKeyGenerate(ctx context.Context, a map[string]interface{}) (string, error) {
	keyType := "ed25519"
	if kt := argString(a, "type"); kt != "" {
		keyType = kt
	}
	keyPath := filepath.Join(g.root, ".ssh", "agent_key")
	if kp := argString(a, "path"); kp != "" {
		keyPath = kp
	}
	parentDir := filepath.Dir(keyPath)
	if err := os.MkdirAll(parentDir, 0700); err != nil {
		return "", fmt.Errorf("error creating directory %s: %v", parentDir, err)
	}
	if _, err := os.Stat(keyPath); err == nil {
		pubData, _ := os.ReadFile(keyPath + ".pub")
		return fmt.Sprintf("Key already exists at %s\nPublic key:\n%s", keyPath, strings.TrimSpace(string(pubData))), nil
	}
	cctx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()
	output, err := exec.CommandContext(cctx, "ssh-keygen", "-t", keyType, "-f", keyPath, "-N", "", "-C", "agent").CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(output))
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("error generating key: %s", msg)
	}
	pubData, err := os.ReadFile(keyPath + ".pub")
	if err != nil {
		return fmt.Sprintf("Key generated at %s but failed to read public key: %v", keyPath, err), nil
	}
	return fmt.Sprintf("SSH key generated:\nPrivate key: %s\nPublic key:\n%s\n\nNext step: add the public key to your GitHub account at https://github.com/settings/keys", keyPath, strings.TrimSpace(string(pubData))), nil
}

func (g *gitTools) sshVerify(ctx context.Context, _ map[string]interface{}) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "ssh", "-i", g.sshKeyPath(), "-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null", "git@github.com")
	output, err := cmd.CombinedOutput()
	result := strings.TrimSpace(string(output))
	if err != nil {
		// ssh -T returns non-zero on auth failure, which is expected
		if result == "" {
			result = err.Error()
		}
		return "", fmt.Errorf("SSH verification failed:\n%s", result)
	}
	return fmt.Sprintf("SSH verification successful:\n%s", result), nil
}

func (g *gitTools) sshKeyList(ctx context.Context, _ map[string]interface{}) (string, error) {
	sshDir := filepath.Join(g.root, ".ssh")
	entries, err := os.ReadDir(sshDir)
	if err != nil {
		return fmt.Sprintf("No SSH directory found at %s", sshDir), nil
	}

	var keys, privKeys, pubKeys []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".pub") {
			pubKeys = append(pubKeys, name)
			keys = append(keys, name)
		} else if !strings.HasPrefix(name, ".") {
			privKeys = append(privKeys, name)
			keys = append(keys, name)
		}
	}

	var parts []string
	if len(keys) > 0 {
		parts = append(parts, fmt.Sprintf("### SSH Key Files\nFound %d key(s): %s", len(keys), strings.Join(keys, ", ")))
	}
	if len(privKeys) > 0 {
		parts = append(parts, fmt.Sprintf("### Private Keys\n%s", strings.Join(privKeys, "\n")))
	}
	if len(pubKeys) > 0 {
		parts = append(parts, fmt.Sprintf("### Public Keys\n%s", strings.Join(pubKeys, "\n")))
	}
	parts = append(parts, fmt.Sprintf("### Active Key\n%s", g.sshKeyPath()))

	return strings.Join(parts, "\n\n"), nil
}

// --- schemas (mirror goclaw's Git*Schema exactly) ---

const repoPathDesc = "Path to the git repository. Must be specified — can be absolute (e.g., /path/to/repo) or relative to the current working directory."

func gitEmptySchema() map[string]interface{} {
	return map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
}

func gitStatusSchema() map[string]interface{} {
	return obj(map[string]interface{}{
		"repo_path": strProp(repoPathDesc),
		"stat":      map[string]interface{}{"type": "boolean", "description": "Use --stat format instead of --short (default false)"},
	}, "repo_path")
}

func gitDiffSchema() map[string]interface{} {
	return obj(map[string]interface{}{
		"repo_path": strProp(repoPathDesc),
		"path":      strProp("Limit diff to this file or directory path"),
		"cached":    map[string]interface{}{"type": "boolean", "description": "Show staged changes (--cached)"},
		"staged":    map[string]interface{}{"type": "boolean", "description": "Alias for cached"},
	}, "repo_path")
}

func gitLogSchema() map[string]interface{} {
	return obj(map[string]interface{}{
		"repo_path": strProp(repoPathDesc),
		"max":       map[string]interface{}{"type": "integer", "description": "Maximum number of commits to show (default 20)"},
		"oneline":   map[string]interface{}{"type": "boolean", "description": "Use --oneline format (default true for brevity)"},
		"path":      strProp("Limit commits to those affecting this file or directory"),
		"branch":    strProp("Show commits reachable from this branch"),
	}, "repo_path")
}

func gitShowSchema() map[string]interface{} {
	return obj(map[string]interface{}{
		"repo_path": strProp(repoPathDesc),
		"ref":       strProp("Commit ref, branch name, or tag to show"),
	}, "ref", "repo_path")
}

func gitBranchSchema() map[string]interface{} {
	return obj(map[string]interface{}{
		"repo_path": strProp(repoPathDesc),
		"local":     map[string]interface{}{"type": "boolean", "description": "Show only local branches (default: show all including remote)"},
	}, "repo_path")
}

func gitAddSchema() map[string]interface{} {
	return obj(map[string]interface{}{
		"repo_path": strProp(repoPathDesc),
		"paths":     map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "List of file paths to stage"},
	}, "paths", "repo_path")
}

func gitCommitSchema() map[string]interface{} {
	return obj(map[string]interface{}{
		"repo_path": strProp(repoPathDesc),
		"message":   strProp("Commit message"),
		"amend":     map[string]interface{}{"type": "boolean", "description": "Amend the last commit instead of creating a new one"},
		"no_verify": map[string]interface{}{"type": "boolean", "description": "Skip pre-commit hooks"},
	}, "message", "repo_path")
}

func gitPushSchema() map[string]interface{} {
	return obj(map[string]interface{}{
		"repo_path":    strProp(repoPathDesc),
		"remote":       strProp("Remote name (default: origin)"),
		"ref":          strProp("Branch ref to push (default: current branch)"),
		"set_upstream": map[string]interface{}{"type": "boolean", "description": "Set upstream tracking info (--set-upstream)"},
		"force":        map[string]interface{}{"type": "boolean", "description": "Force push (--force)"},
	}, "repo_path")
}

func gitPullSchema() map[string]interface{} {
	return obj(map[string]interface{}{
		"repo_path": strProp(repoPathDesc),
		"remote":    strProp("Remote name (default: origin)"),
		"ref":       strProp("Branch ref to pull (default: current branch)"),
		"rebase":    map[string]interface{}{"type": "boolean", "description": "Pull with rebase (--rebase)"},
	}, "repo_path")
}

func gitRemoteSchema() map[string]interface{} {
	return obj(map[string]interface{}{
		"repo_path": strProp(repoPathDesc),
	}, "repo_path")
}

func gitRevParseSchema() map[string]interface{} {
	return obj(map[string]interface{}{
		"repo_path": strProp(repoPathDesc),
		"ref":       strProp("Ref to resolve (e.g., HEAD, branch name)"),
	}, "ref", "repo_path")
}

func gitMergeSchema() map[string]interface{} {
	return obj(map[string]interface{}{
		"repo_path": strProp(repoPathDesc),
		"branch":    strProp("Branch name to merge into current branch"),
	}, "branch", "repo_path")
}

func gitCheckoutSchema() map[string]interface{} {
	return obj(map[string]interface{}{
		"repo_path": strProp(repoPathDesc),
		"branch":    strProp("Branch name to checkout"),
		"create":    map[string]interface{}{"type": "boolean", "description": "Create a new branch (-b flag)"},
		"path":      strProp("File path to restore from HEAD"),
	}, "branch", "repo_path")
}

func gitCloneSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"url":       strProp("Repository URL to clone (e.g., git@github.com:user/repo.git)"),
			"directory": strProp("Subdirectory within workspace to clone into (defaults to workspace root)"),
		},
	}
}

func gitSSHKeyGenerateSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"type": strProp("Key type: ed25519 (default), rsa, ecdsa, or dsa"),
			"path": strProp("Output path for the private key (default: workspace/.ssh/agent_key)"),
		},
	}
}
