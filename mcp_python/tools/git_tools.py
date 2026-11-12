"""Git + GitHub CLI tools — mirror of internal/tools/git.go + gh.go."""
import os, subprocess, pathlib, shutil
from typing import Annotated
from mcp_python.tools._registry import mcp

WS = lambda: pathlib.Path(os.environ.get("MYMCP_WORKSPACE", str(pathlib.Path.home() / "mymcp-workspace"))).resolve()

def _run(cmd, **kw):
    r = subprocess.run(cmd, shell=isinstance(cmd, str), capture_output=True, text=True, timeout=kw.pop("timeout", 30), cwd=str(WS()), **kw)
    return (r.stdout + r.stderr)[:8000] or f"(exit {r.returncode})"

@mcp.tool(name="git_query", description="Run a git command in the workspace (e.g. 'status --short', 'log --oneline -5').")
def git_query(args: Annotated[str, "Arguments to pass to git, e.g. 'status --short'"]) -> str:
    return _run(f"git {args}")

# GitHub CLI passthrough — mirrors gh.go's ghRun helper
@mcp.tool(name="gh_repo_view", description="Show GitHub repo info via gh.")
def gh_repo_view(repo: str = "") -> str:
    return _run(f"gh repo view {repo} --json name,description,updatedAt" if repo else "gh repo view --json name,description,updatedAt")

@mcp.tool(name="gh_pr_list", description="List pull requests.")
def gh_pr_list(repo: str = "", state: str = "open", limit: int = 20) -> str:
    repo_arg = f" --repo {repo}" if repo else ""
    return _run(f"gh pr list{repo_arg} --state {state} --limit {limit}")

@mcp.tool(name="gh_pr_view", description="Show PR details.")
def gh_pr_view(number: int, repo: str = "") -> str:
    return _run(f"gh pr view {number} {'--repo ' + repo if repo else ''}")

@mcp.tool(name="gh_pr_diff", description="Show PR diff.")
def gh_pr_diff(number: int, repo: str = "") -> str:
    return _run(f"gh pr diff {number} {'--repo ' + repo if repo else ''}")

@mcp.tool(name="gh_pr_create", description="Create a pull request.")
def gh_pr_create(title: str, body: str = "", repo: str = "") -> str:
    return _run(f"gh pr create --title {title!r} --body {body!r} {'--repo ' + repo if repo else ''}")

@mcp.tool(name="gh_pr_merge", description="Merge a PR.")
def gh_pr_merge(number: int, repo: str = "") -> str:
    return _run(f"gh pr merge {number} {'--repo ' + repo if repo else ''}")

@mcp.tool(name="gh_pr_check", description="Show PR checks.")
def gh_pr_check(number: int, repo: str = "") -> str:
    return _run(f"gh pr checks {number} {'--repo ' + repo if repo else ''}")

@mcp.tool(name="gh_issue_list", description="List issues.")
def gh_issue_list(repo: str = "", state: str = "open", limit: int = 20) -> str:
    return _run(f"gh issue list {'--repo ' + repo if repo else ''} --state {state} --limit {limit}")

@mcp.tool(name="gh_issue_view", description="Show issue details.")
def gh_issue_view(number: int, repo: str = "") -> str:
    return _run(f"gh issue view {number} {'--repo ' + repo if repo else ''}")

@mcp.tool(name="gh_issue_create", description="Create an issue.")
def gh_issue_create(title: str, body: str = "", repo: str = "") -> str:
    return _run(f"gh issue create --title {title!r} --body {body!r} {'--repo ' + repo if repo else ''}")

@mcp.tool(name="gh_issue_comment", description="Comment on an issue/PR.")
def gh_issue_comment(number: int, body: str, repo: str = "") -> str:
    return _run(f"gh issue comment {number} --body {body!r} {'--repo ' + repo if repo else ''}")

@mcp.tool(name="gh_run_list", description="List workflow runs.")
def gh_run_list(repo: str = "", limit: int = 10) -> str:
    return _run(f"gh run list {'--repo ' + repo if repo else ''} --limit {limit}")

@mcp.tool(name="gh_run_view", description="Show workflow run details.")
def gh_run_view(run_id: str, repo: str = "") -> str:
    return _run(f"gh run view {run_id} {'--repo ' + repo if repo else ''}")

@mcp.tool(name="gh_run_rerun", description="Rerun a workflow run.")
def gh_run_rerun(run_id: str, repo: str = "") -> str:
    return _run(f"gh run rerun {run_id} {'--repo ' + repo if repo else ''}")
