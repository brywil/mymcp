"""System/process/tmux/misc tools — mirrors internal/tools/{system,tmux,recall,wait,thinking,memory}.py"""

import os, subprocess, pathlib, time, json, shutil
from typing import Annotated
from mcp_python.tools._registry import mcp

WS = lambda: pathlib.Path(os.environ.get("MYMCP_WORKSPACE", str(pathlib.Path.home() / "mymcp-workspace"))).resolve()

# -- tmux / sessions (thin wrappers over shell) -------------------------------
@mcp.tool(name="list_sessions", description="List tmux sessions.")
def list_sessions() -> str:
    r = subprocess.run(["tmux", "list-sessions"], capture_output=True, text=True, timeout=5)
    return r.stdout or r.stderr or "(no sessions)"

@mcp.tool(name="create_session", description="Create a tmux session.")
def create_session(name: str) -> str:
    r = subprocess.run(["tmux", "new-session", "-d", "-s", name], capture_output=True, text=True, timeout=5)
    return r.stdout + r.stderr or f"Created session {name}"

@mcp.tool(name="kill_session", description="Kill a tmux session.")
def kill_session(name: str) -> str:
    r = subprocess.run(["tmux", "kill-session", "-t", name], capture_output=True, text=True, timeout=5)
    return r.stdout + r.stderr or f"Killed {name}"

@mcp.tool(name="send_keys", description="Send keys/input to a tmux pane.")
def send_keys(session: str, keys: str) -> str:
    r = subprocess.run(["tmux", "send-keys", "-t", session, keys, "Enter"], capture_output=True, text=True, timeout=5)
    return r.stdout + r.stderr or "sent"

@mcp.tool(name="capture_pane", description="Capture tmux pane contents.")
def capture_pane(session: str) -> str:
    r = subprocess.run(["tmux", "capture-pane", "-p", "-t", session], capture_output=True, text=True, timeout=5)
    return r.stdout[:8000] or r.stderr

@mcp.tool(name="wait_for", description="Wait for a file/condition with timeout.")
def wait_for(path: str = "", pattern: str = "", timeout_ms: int = 10000) -> str:
    deadline = time.time() + timeout_ms / 1000
    p = WS() / path if path and not path.startswith("/") else pathlib.Path(path) if path else None
    while time.time() < deadline:
        if p and p.exists():
            if not pattern or pattern in p.read_text(errors="replace"):
                return f"Ready: {p}"
        time.sleep(0.5)
    return f"Timeout after {timeout_ms}ms waiting for {path}"

# -- memory / recall (simple file-backed stub) --------------------------------
MEM_DIR = pathlib.Path.home() / ".local" / "share" / "mymcp-memory"

@mcp.tool(name="memory_save", description="Save a memory snippet.")
def memory_save(content: str, key: str = "") -> str:
    MEM_DIR.mkdir(parents=True, exist_ok=True)
    name = key or f"mem-{int(time.time())}"
    (MEM_DIR / f"{name}.md").write_text(content)
    return f"Saved as {name}"

@mcp.tool(name="memory_search", description="Search saved memories.")
def memory_search(query: str) -> str:
    hits = []
    for f in MEM_DIR.glob("*.md"):
        if query.lower() in f.read_text().lower():
            hits.append(f.name)
    return "\n".join(hits[:20]) or "(no matches)"

@mcp.tool(name="memory_read", description="Read a saved memory.")
def memory_read(key: str) -> str:
    return (MEM_DIR / f"{key}.md").read_text() if (MEM_DIR / f"{key}.md").exists() else f"Not found: {key}"

@mcp.tool(name="memory_list", description="List saved memories.")
def memory_list() -> str:
    return "\n".join(p.name for p in MEM_DIR.glob("*.md")) or "(empty)"

@mcp.tool(name="recall", description="Recall context (alias for memory_search).")
def recall(query: str) -> str:
    return memory_search(query)

# -- thinking / sequentialthinking -------------------------------------------
@mcp.tool(name="sequentialthinking", description="Structured thinking trace (logs the thought, returns ack).")
def sequentialthinking(thought: str, next_step: str = "") -> str:
    return f"Thought recorded ({len(thought)} chars). Next: {next_step}"

@mcp.tool(name="note", description="Save a note (alias for memory_save).")
def note(content: str) -> str:
    return memory_save(content)

# -- misc compat --------------------------------------------------------------
@mcp.tool(name="list_tools", description="List available tools (mirrors Go's list_tools).")
def list_tools() -> str:
    from mcp_python.tools._registry import mcp as _mcp
    tools = getattr(_mcp, "_tools", {}) or {}
    return "\n".join(sorted(tools.keys())) if tools else "tools registered at startup (see README)"

