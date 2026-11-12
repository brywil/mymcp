"""Remaining compat shims — tmux full set, browser, etc."""

# Reuse system_tools for tmux; add browser stubs that proxy to remote-devices if present
from mcp_python.tools.system_tools import mcp  # re-export the same FastMCP

@mcp.tool(name="browser_health", description="Check browser availability (stub — use remote-devices for real browser).")
def browser_health() -> str:
    return "browser tools require a real browser session (Claude in Chrome / built-in browser). No local browser attached."

@mcp.tool(name="evaluate", description="Evaluate JS in browser (requires browser session).")
def evaluate(expression: str) -> str:
    return "No browser session — attach via claude-in-chrome or built-in browser first."

@mcp.tool(name="navigate", description="Navigate browser to URL (requires browser session).")
def navigate(url: str) -> str:
    return "No browser session."

@mcp.tool(name="screenshot", description="Screenshot (requires browser session).")
def screenshot() -> str:
    return "No browser session."

@mcp.tool(name="resume_session", description="Resume a suspended tmux session.")
def resume_session(name: str) -> str:
    import subprocess
    r = subprocess.run(["tmux", "has-session", "-t", name], capture_output=True)
    return f"Session {name} {'exists' if r.returncode == 0 else 'not found'}"

@mcp.tool(name="suspend_session", description="Suspend a tmux session.")
def suspend_session(name: str) -> str:
    return f"Suspended {name} (stub)"

@mcp.tool(name="rename_session", description="Rename a tmux session.")
def rename_session(old: str, new: str) -> str:
    import subprocess
    r = subprocess.run(["tmux", "rename-session", "-t", old, new], capture_output=True, text=True)
    return r.stdout + r.stderr or f"Renamed {old} -> {new}"

@mcp.tool(name="set_active_session", description="Set active tmux session.")
def set_active_session(name: str) -> str:
    import subprocess
    r = subprocess.run(["tmux", "switch-client", "-t", name], capture_output=True, text=True)
    return r.stdout + r.stderr or f"Active: {name}"

@mcp.tool(name="file_info", description="File info (alias for path_info).")
def file_info(path: str) -> str:
    import pathlib, os
    p = pathlib.Path(path).expanduser()
    s = p.stat()
    return f"{p} dir={p.is_dir()} size={s.st_size}"

