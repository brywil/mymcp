package tools

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// gitTools exposes a single READ-ONLY git query tool.
//
// The previous git suite (18 tools: commit, push, merge, checkout, clone, add,
// pull, and SSH key generation) was removed because development moved to
// opencode — goclaw observes, opencode edits. That was right, but it took the
// information-gathering half with it: an agent could no longer answer "what
// changed?", "what version is this?", or "when did that break?" about a repo,
// including its own.
//
// So this restores reading and nothing else. One tool rather than a suite,
// because every registered tool costs tokens in the system prompt on EVERY turn,
// and on a bandwidth-bound host prefill dominates decode — a handful of
// subcommands is not worth a handful of schema entries.
//
// The allowlist is the enforcement. A model cannot be relied on to stay
// read-only because it was asked to, and `git` has enough mutating subcommands
// (and enough ways to smuggle one through an option) that an explicit set of
// permitted verbs is the only honest boundary.
type gitTools struct {
	root string
}

const gitMaxOutput = 8000

// readOnlyGitVerbs are the subcommands this tool will run. Anything absent is
// refused rather than attempted: adding a verb here is a deliberate act.
//
// Notably excluded even though they only *usually* read: `stash` (mutates the
// working tree), `clean` (deletes), `fetch`/`pull` (network + ref updates),
// `checkout`/`switch`/`restore` (mutate), `config` (writes with --global), and
// `submodule` (runs arbitrary child commands).
var readOnlyGitVerbs = map[string]string{
	"log":       "commit history",
	"diff":      "changes between commits/worktree",
	"show":      "a commit, tag or object",
	"status":    "working tree state",
	"blame":     "line-by-line authorship",
	"describe":  "nearest tag for a commit",
	"rev-parse": "resolve a revision to a SHA",
	"shortlog":  "commit counts by author",
	"tag":       "list tags (listing only; see the -a/-d guard below)",
	"branch":    "list branches (listing only; see the -d/-m guard below)",
	"remote":    "list remotes",
}

// mutatingFlags are refused on otherwise-read-only verbs. `git tag -d`, `git
// branch -D` and `git remote add` all mutate despite living under a verb whose
// bare form only lists.
var mutatingFlags = []string{
	"-d", "-D", "--delete", "-m", "-M", "--move", "-a", "--annotate",
	"add", "remove", "rename", "set-url", "prune", "set-head", "-f", "--force",
}

func (g *gitTools) register(r *Registry) {
	r.Register(&Tool{
		Name:        "git_query",
		Description: gitQueryDescription(),
		Schema:      gitQuerySchema,
		ReadOnly:    true,
		Handler:     g.query,
	})
}

func gitQueryDescription() string {
	verbs := make([]string, 0, len(readOnlyGitVerbs))
	for v := range readOnlyGitVerbs {
		verbs = append(verbs, v)
	}
	sort.Strings(verbs)
	return "Run a READ-ONLY git command in a repository and return its output. " +
		"Use this to answer questions about a repo — what changed, what version is " +
		"checked out, who touched a line, what is uncommitted. Permitted " +
		"subcommands: " + strings.Join(verbs, ", ") + ". " +
		"Anything that writes (commit, push, checkout, merge, pull, stash, clean, " +
		"config) is refused — make code changes through opencode, not this tool."
}

var gitQuerySchema = map[string]interface{}{
	"type": "object",
	"properties": map[string]interface{}{
		"repo_path": map[string]interface{}{
			"type":        "string",
			"description": "Path to the repository. Relative paths resolve against the workspace root.",
		},
		"subcommand": map[string]interface{}{
			"type":        "string",
			"description": "The git subcommand, e.g. 'log', 'diff', 'show', 'status', 'blame', 'describe', 'rev-parse'.",
		},
		"args": map[string]interface{}{
			"type":        "array",
			"items":       map[string]interface{}{"type": "string"},
			"description": "Arguments for the subcommand, e.g. ['--oneline','-10'] or ['abc123..def456','--stat']. Each argument must be a separate array element, not one space-joined string.",
		},
	},
	"required": []string{"repo_path", "subcommand"},
}

func (g *gitTools) query(ctx context.Context, args map[string]interface{}) (string, error) {
	repo, err := g.repoPath(args)
	if err != nil {
		return "", err
	}

	sub, _ := args["subcommand"].(string)
	sub = strings.TrimSpace(strings.ToLower(sub))
	if sub == "" {
		return "", fmt.Errorf("'subcommand' is required")
	}
	if _, ok := readOnlyGitVerbs[sub]; !ok {
		return "", fmt.Errorf("git %s is not permitted here — this tool is read-only. "+
			"Permitted: %s. To change code, use opencode", sub, strings.Join(sortedVerbs(), ", "))
	}

	extra, err := gitStringSlice(args["args"])
	if err != nil {
		return "", err
	}
	// Refuse mutating flags on verbs whose bare form only reads, and refuse
	// anything that would let git run a different command or escape the repo.
	for _, a := range extra {
		la := strings.ToLower(strings.TrimSpace(a))
		for _, bad := range mutatingFlags {
			if la == bad {
				return "", fmt.Errorf("argument %q would modify the repository; this tool is read-only", a)
			}
		}
		if strings.HasPrefix(la, "--exec") || strings.HasPrefix(la, "--upload-pack") ||
			strings.HasPrefix(la, "--receive-pack") || la == "-c" || strings.HasPrefix(la, "--git-dir") ||
			strings.HasPrefix(la, "--work-tree") {
			return "", fmt.Errorf("argument %q is not allowed (it can run other commands or retarget the repo)", a)
		}
	}

	cmdArgs := append([]string{sub}, extra...)
	cmd := exec.CommandContext(ctx, "git", cmdArgs...)
	cmd.Dir = repo
	// Never prompt: a credential or editor prompt would hang until the timeout.
	cmd.Env = append(cmd.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_PAGER=cat", "PAGER=cat")

	out, runErr := cmd.CombinedOutput()
	result := strings.TrimSpace(string(out))
	if runErr != nil {
		if result == "" {
			result = runErr.Error()
		}
		return "", fmt.Errorf("git %s failed: %s", strings.Join(cmdArgs, " "), result)
	}
	if result == "" {
		return "(no output)", nil
	}
	if len(result) > gitMaxOutput {
		result = result[:gitMaxOutput] + "\n\n... (truncated — narrow the query, e.g. add -n or a path)"
	}
	return result, nil
}

// repoPath resolves repo_path against the workspace root, matching ghTools.
func (g *gitTools) repoPath(args map[string]interface{}) (string, error) {
	repo := argString(args, "repo_path")
	if repo == "" {
		return "", fmt.Errorf("repo_path is required — the path to the git repository")
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

func sortedVerbs() []string {
	verbs := make([]string, 0, len(readOnlyGitVerbs))
	for v := range readOnlyGitVerbs {
		verbs = append(verbs, v)
	}
	sort.Strings(verbs)
	return verbs
}

// stringSlice coerces the JSON args array into []string.
func gitStringSlice(v interface{}) ([]string, error) {
	if v == nil {
		return nil, nil
	}
	raw, ok := v.([]interface{})
	if !ok {
		return nil, fmt.Errorf("'args' must be an array of strings")
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("'args' must contain only strings")
		}
		out = append(out, s)
	}
	return out, nil
}
