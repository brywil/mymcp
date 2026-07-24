package tools

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// ghTools wraps the GitHub CLI (gh), scoped to the workspace root.
type ghTools struct {
	root string
}

const ghMaxOutput = 8000

func (g *ghTools) register(r *Registry) {
	r.Register(&Tool{Name: "gh_pr_list", Description: "List pull requests.", Schema: prListSchema, ReadOnly: true, Handler: g.prList})
	r.Register(&Tool{Name: "gh_pr_view", Description: "Show details of a pull request.", Schema: prViewSchema, ReadOnly: true, Handler: g.prView})
	r.Register(&Tool{Name: "gh_pr_create", Description: "Create a new pull request.", Schema: prCreateSchema, Handler: g.prCreate})
	r.Register(&Tool{Name: "gh_pr_merge", Description: "Merge a pull request.", Schema: prMergeSchema, Handler: g.prMerge})
	r.Register(&Tool{Name: "gh_pr_check", Description: "Show PR checks.", Schema: prCheckSchema, ReadOnly: true, Handler: g.prCheck})
	r.Register(&Tool{Name: "gh_pr_diff", Description: "Show the diff of a PR.", Schema: prDiffSchema, ReadOnly: true, Handler: g.prDiff})
	r.Register(&Tool{Name: "gh_issue_list", Description: "List issues.", Schema: issueListSchema, ReadOnly: true, Handler: g.issueList})
	r.Register(&Tool{Name: "gh_issue_view", Description: "Show details of an issue.", Schema: issueViewSchema, ReadOnly: true, Handler: g.issueView})
	r.Register(&Tool{Name: "gh_issue_create", Description: "Create a new issue.", Schema: issueCreateSchema, Handler: g.issueCreate})
	r.Register(&Tool{Name: "gh_issue_comment", Description: "Add a comment to an issue or PR.", Schema: issueCommentSchema, Handler: g.issueComment})
	r.Register(&Tool{Name: "gh_run_list", Description: "List workflow runs.", Schema: runListSchema, ReadOnly: true, Handler: g.runList})
	r.Register(&Tool{Name: "gh_run_view", Description: "Show details of a workflow run.", Schema: runViewSchema, ReadOnly: true, Handler: g.runView})
	r.Register(&Tool{Name: "gh_run_rerun", Description: "Rerun a workflow run.", Schema: runRerunSchema, Handler: g.runRerun})
	r.Register(&Tool{Name: "gh_repo_view", Description: "Show repository info.", Schema: repoViewSchema, ReadOnly: true, Handler: g.repoView})
}

// ghRun executes a gh command in the given repo directory and returns truncated output.
func (g *ghTools) ghRun(ctx context.Context, repo string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = repo
	output, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(output))
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("gh command failed: %s — %s", strings.Join(args, " "), msg)
	}
	result := strings.TrimSpace(string(output))
	if len(result) > ghMaxOutput {
		result = result[:ghMaxOutput] + "\n\n... (truncated, use more specific query to see more)"
	}
	return result, nil
}

// getRepoPath returns the repo path from args, resolving relative paths against
// the workspace root. Returns an error if not provided.
func (g *ghTools) getRepoPath(args map[string]interface{}) (string, error) {
	repo := argString(args, "repo_path")
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

func (g *ghTools) prList(ctx context.Context, a map[string]interface{}) (string, error) {
	repo, err := g.getRepoPath(a)
	if err != nil {
		return "", err
	}
	var cmdArgs []string
	if s := argString(a, "state"); s != "" {
		cmdArgs = append(cmdArgs, "--state", s)
	}
	if s := argString(a, "author"); s != "" {
		cmdArgs = append(cmdArgs, "--author", s)
	}
	if s := argString(a, "label"); s != "" {
		cmdArgs = append(cmdArgs, "--label", s)
	}
	if s := argString(a, "base"); s != "" {
		cmdArgs = append(cmdArgs, "--base", s)
	}
	if s := argString(a, "search"); s != "" {
		cmdArgs = append(cmdArgs, "--search", s)
	}
	cmdArgs = append(cmdArgs, "--limit", fmt.Sprintf("%d", argInt(a, "limit", 30)))
	cmdArgs = append([]string{"pr", "list"}, cmdArgs...)
	return g.ghRun(ctx, repo, cmdArgs...)
}

func (g *ghTools) prView(ctx context.Context, a map[string]interface{}) (string, error) {
	repo, err := g.getRepoPath(a)
	if err != nil {
		return "", err
	}
	pr, ok := a["number"].(float64)
	if !ok {
		return "", fmt.Errorf("number is required — PR number (e.g., 42)")
	}
	var cmdArgs []string
	if argBool(a, "json", false) {
		cmdArgs = append(cmdArgs, "--json", "number,title,state,author,createdAt,updatedAt,mergeable,statusCheckRollup,comments,reviews,files,additions,deletions,commits,labels")
	}
	if argBool(a, "web", false) {
		cmdArgs = append(cmdArgs, "--web")
	}
	cmdArgs = append([]string{"pr", "view", fmt.Sprintf("%d", int(pr))}, cmdArgs...)
	return g.ghRun(ctx, repo, cmdArgs...)
}

func (g *ghTools) prCreate(ctx context.Context, a map[string]interface{}) (string, error) {
	repo, err := g.getRepoPath(a)
	if err != nil {
		return "", err
	}
	title := argString(a, "title")
	if title == "" {
		return "", fmt.Errorf("title is required")
	}
	cmdArgs := []string{"--title", title}
	if s := argString(a, "body"); s != "" {
		cmdArgs = append(cmdArgs, "--body", s)
	}
	if s := argString(a, "base"); s != "" {
		cmdArgs = append(cmdArgs, "--base", s)
	}
	if s := argString(a, "head"); s != "" {
		cmdArgs = append(cmdArgs, "--head", s)
	}
	if argBool(a, "draft", false) {
		cmdArgs = append(cmdArgs, "--draft")
	}
	if argBool(a, "dry_run", false) {
		cmdArgs = append(cmdArgs, "--dry-run")
	}
	cmdArgs = append([]string{"pr", "create"}, cmdArgs...)
	return g.ghRun(ctx, repo, cmdArgs...)
}

func (g *ghTools) prMerge(ctx context.Context, a map[string]interface{}) (string, error) {
	repo, err := g.getRepoPath(a)
	if err != nil {
		return "", err
	}
	pr, ok := a["number"].(float64)
	if !ok {
		return "", fmt.Errorf("number is required — PR number")
	}
	cmdArgs := []string{fmt.Sprintf("%d", int(pr))}
	if argBool(a, "delete_branch", false) {
		cmdArgs = append(cmdArgs, "--delete-branch")
	}
	if argBool(a, "squash", false) {
		cmdArgs = append(cmdArgs, "--squash")
	}
	if argBool(a, "rebase", false) {
		cmdArgs = append(cmdArgs, "--rebase")
	}
	if argBool(a, "merge_commit", false) {
		cmdArgs = append(cmdArgs, "--merge")
	}
	cmdArgs = append([]string{"pr", "merge"}, cmdArgs...)
	return g.ghRun(ctx, repo, cmdArgs...)
}

func (g *ghTools) prCheck(ctx context.Context, a map[string]interface{}) (string, error) {
	repo, err := g.getRepoPath(a)
	if err != nil {
		return "", err
	}
	pr, ok := a["number"].(float64)
	if !ok {
		return "", fmt.Errorf("number is required — PR number")
	}
	return g.ghRun(ctx, repo, "pr", "checks", fmt.Sprintf("%d", int(pr)))
}

func (g *ghTools) prDiff(ctx context.Context, a map[string]interface{}) (string, error) {
	repo, err := g.getRepoPath(a)
	if err != nil {
		return "", err
	}
	pr, ok := a["number"].(float64)
	if !ok {
		return "", fmt.Errorf("number is required — PR number")
	}
	return g.ghRun(ctx, repo, "pr", "diff", fmt.Sprintf("%d", int(pr)))
}

func (g *ghTools) issueList(ctx context.Context, a map[string]interface{}) (string, error) {
	repo, err := g.getRepoPath(a)
	if err != nil {
		return "", err
	}
	var cmdArgs []string
	if s := argString(a, "state"); s != "" {
		cmdArgs = append(cmdArgs, "--state", s)
	}
	if s := argString(a, "assignee"); s != "" {
		cmdArgs = append(cmdArgs, "--assignee", s)
	}
	if s := argString(a, "label"); s != "" {
		cmdArgs = append(cmdArgs, "--label", s)
	}
	if s := argString(a, "search"); s != "" {
		cmdArgs = append(cmdArgs, "--search", s)
	}
	cmdArgs = append(cmdArgs, "--limit", fmt.Sprintf("%d", argInt(a, "limit", 30)))
	cmdArgs = append([]string{"issue", "list"}, cmdArgs...)
	return g.ghRun(ctx, repo, cmdArgs...)
}

func (g *ghTools) issueView(ctx context.Context, a map[string]interface{}) (string, error) {
	repo, err := g.getRepoPath(a)
	if err != nil {
		return "", err
	}
	issue, ok := a["number"].(float64)
	if !ok {
		return "", fmt.Errorf("number is required — issue number")
	}
	var cmdArgs []string
	if argBool(a, "json", false) {
		cmdArgs = append(cmdArgs, "--json", "number,title,state,body,createdAt,updatedAt,closedAt,labels,comments,assignees,reactions")
	}
	cmdArgs = append([]string{"issue", "view", fmt.Sprintf("%d", int(issue))}, cmdArgs...)
	return g.ghRun(ctx, repo, cmdArgs...)
}

func (g *ghTools) issueCreate(ctx context.Context, a map[string]interface{}) (string, error) {
	repo, err := g.getRepoPath(a)
	if err != nil {
		return "", err
	}
	title := argString(a, "title")
	if title == "" {
		return "", fmt.Errorf("title is required")
	}
	cmdArgs := []string{"--title", title}
	if s := argString(a, "body"); s != "" {
		cmdArgs = append(cmdArgs, "--body", s)
	}
	if s := argString(a, "label"); s != "" {
		cmdArgs = append(cmdArgs, "--label", s)
	}
	if s := argString(a, "assignee"); s != "" {
		cmdArgs = append(cmdArgs, "--assignee", s)
	}
	if s := argString(a, "milestone"); s != "" {
		cmdArgs = append(cmdArgs, "--milestone", s)
	}
	cmdArgs = append([]string{"issue", "create"}, cmdArgs...)
	return g.ghRun(ctx, repo, cmdArgs...)
}

func (g *ghTools) issueComment(ctx context.Context, a map[string]interface{}) (string, error) {
	repo, err := g.getRepoPath(a)
	if err != nil {
		return "", err
	}
	number, ok := a["number"].(float64)
	if !ok {
		return "", fmt.Errorf("number is required — issue or PR number")
	}
	body := argString(a, "body")
	if body == "" {
		return "", fmt.Errorf("body is required — comment text")
	}
	return g.ghRun(ctx, repo, "issue", "comment", fmt.Sprintf("%d", int(number)), "--body", body)
}

func (g *ghTools) runList(ctx context.Context, a map[string]interface{}) (string, error) {
	repo, err := g.getRepoPath(a)
	if err != nil {
		return "", err
	}
	var cmdArgs []string
	if s := argString(a, "branch"); s != "" {
		cmdArgs = append(cmdArgs, "--branch", s)
	}
	if s := argString(a, "status"); s != "" {
		cmdArgs = append(cmdArgs, "--status", s)
	}
	cmdArgs = append(cmdArgs, "--limit", fmt.Sprintf("%d", argInt(a, "limit", 10)))
	cmdArgs = append([]string{"run", "list"}, cmdArgs...)
	return g.ghRun(ctx, repo, cmdArgs...)
}

func (g *ghTools) runView(ctx context.Context, a map[string]interface{}) (string, error) {
	repo, err := g.getRepoPath(a)
	if err != nil {
		return "", err
	}
	run, ok := a["id"].(float64)
	if !ok {
		return "", fmt.Errorf("id is required — workflow run ID")
	}
	var cmdArgs []string
	if argBool(a, "json", false) {
		cmdArgs = append(cmdArgs, "--json", "id,status,conclusion,headBranch,headSha,createdAt,updatedAt,runNumber")
	}
	cmdArgs = append([]string{"run", "view", fmt.Sprintf("%d", int(run))}, cmdArgs...)
	return g.ghRun(ctx, repo, cmdArgs...)
}

func (g *ghTools) runRerun(ctx context.Context, a map[string]interface{}) (string, error) {
	repo, err := g.getRepoPath(a)
	if err != nil {
		return "", err
	}
	run, ok := a["id"].(float64)
	if !ok {
		return "", fmt.Errorf("id is required — workflow run ID")
	}
	return g.ghRun(ctx, repo, "run", "rerun", fmt.Sprintf("%d", int(run)))
}

func (g *ghTools) repoView(ctx context.Context, a map[string]interface{}) (string, error) {
	repo, err := g.getRepoPath(a)
	if err != nil {
		return "", err
	}
	var cmdArgs []string
	if argBool(a, "json", false) {
		cmdArgs = append(cmdArgs, "--json", "name,owner,description,url,createdAt,defaultBranchRef,forkCount,openIssues,stargazerCount")
	}
	cmdArgs = append([]string{"repo", "view"}, cmdArgs...)
	return g.ghRun(ctx, repo, cmdArgs...)
}

// --- schemas (faithful to goclaw's gh_tools.go) ---

var ghRepoPathProp = strProp("Path to the git repository. Must be specified — can be absolute (e.g., /path/to/repo) or relative to the current working directory.")

var prListSchema = obj(map[string]interface{}{
	"repo_path": ghRepoPathProp,
	"state":     strProp("Filter by state: open, closed, all (default: open)"),
	"author":    strProp("Filter by author username"),
	"label":     strProp("Filter by label"),
	"base":      strProp("Filter by base branch"),
	"search":    strProp("Search query (gh search syntax)"),
	"limit":     map[string]interface{}{"type": "integer", "description": "Maximum results (default 30)"},
}, "repo_path")

var prViewSchema = obj(map[string]interface{}{
	"repo_path": ghRepoPathProp,
	"number":    map[string]interface{}{"type": "integer", "description": "PR number"},
	"json":      map[string]interface{}{"type": "boolean", "description": "Output as JSON for programmatic use"},
	"web":       map[string]interface{}{"type": "boolean", "description": "Open PR in browser"},
}, "repo_path", "number")

var prCreateSchema = obj(map[string]interface{}{
	"repo_path": ghRepoPathProp,
	"title":     strProp("PR title"),
	"body":      strProp("PR body/description"),
	"base":      strProp("Base branch"),
	"head":      strProp("Head branch (source)"),
	"draft":     map[string]interface{}{"type": "boolean", "description": "Create as draft PR"},
	"dry_run":   map[string]interface{}{"type": "boolean", "description": "Show what would be created without creating"},
}, "repo_path", "title")

var prMergeSchema = obj(map[string]interface{}{
	"repo_path":     ghRepoPathProp,
	"number":        map[string]interface{}{"type": "integer", "description": "PR number"},
	"delete_branch": map[string]interface{}{"type": "boolean", "description": "Delete the source branch after merge"},
	"squash":        map[string]interface{}{"type": "boolean", "description": "Squash commits on merge"},
	"rebase":        map[string]interface{}{"type": "boolean", "description": "Rebase commits on merge"},
	"merge_commit":  map[string]interface{}{"type": "boolean", "description": "Create a merge commit (default)"},
}, "repo_path", "number")

var prCheckSchema = obj(map[string]interface{}{
	"repo_path": ghRepoPathProp,
	"number":    map[string]interface{}{"type": "integer", "description": "PR number"},
}, "repo_path", "number")

var prDiffSchema = obj(map[string]interface{}{
	"repo_path": ghRepoPathProp,
	"number":    map[string]interface{}{"type": "integer", "description": "PR number"},
}, "repo_path", "number")

var issueListSchema = obj(map[string]interface{}{
	"repo_path": ghRepoPathProp,
	"state":     strProp("Filter by state: open, closed, all (default: open)"),
	"assignee":  strProp("Filter by assignee username"),
	"label":     strProp("Filter by label"),
	"search":    strProp("Search query (gh search syntax)"),
	"limit":     map[string]interface{}{"type": "integer", "description": "Maximum results (default 30)"},
}, "repo_path")

var issueViewSchema = obj(map[string]interface{}{
	"repo_path": ghRepoPathProp,
	"number":    map[string]interface{}{"type": "integer", "description": "Issue number"},
	"json":      map[string]interface{}{"type": "boolean", "description": "Output as JSON for programmatic use"},
}, "repo_path", "number")

var issueCreateSchema = obj(map[string]interface{}{
	"repo_path": ghRepoPathProp,
	"title":     strProp("Issue title"),
	"body":      strProp("Issue body/description"),
	"label":     strProp("Label to apply"),
	"assignee":  strProp("Assignee username"),
	"milestone": strProp("Milestone name or number"),
}, "repo_path", "title")

var issueCommentSchema = obj(map[string]interface{}{
	"repo_path": ghRepoPathProp,
	"number":    map[string]interface{}{"type": "integer", "description": "Issue or PR number"},
	"body":      strProp("Comment text"),
}, "repo_path", "number", "body")

var runListSchema = obj(map[string]interface{}{
	"repo_path": ghRepoPathProp,
	"branch":    strProp("Filter by branch"),
	"status":    strProp("Filter by status: queued, in_progress, completed, etc."),
	"limit":     map[string]interface{}{"type": "integer", "description": "Maximum results (default 10)"},
}, "repo_path")

var runViewSchema = obj(map[string]interface{}{
	"repo_path": ghRepoPathProp,
	"id":        map[string]interface{}{"type": "integer", "description": "Workflow run ID"},
	"json":      map[string]interface{}{"type": "boolean", "description": "Output as JSON for programmatic use"},
}, "repo_path", "id")

var runRerunSchema = obj(map[string]interface{}{
	"repo_path": ghRepoPathProp,
	"id":        map[string]interface{}{"type": "integer", "description": "Workflow run ID to rerun"},
}, "repo_path", "id")

var repoViewSchema = obj(map[string]interface{}{
	"repo_path": ghRepoPathProp,
	"json":      map[string]interface{}{"type": "boolean", "description": "Output as JSON for programmatic use"},
}, "repo_path")
