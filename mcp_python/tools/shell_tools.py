"""Shell/system tools — mirror of internal/tools/system.go + presets.go."""

import os, subprocess, pathlib, json, datetime, re, shutil, glob as pyglob
from typing import Annotated
from mcp_python.tools._registry import mcp

WS = lambda: pathlib.Path(os.environ.get("MYMCP_WORKSPACE", str(pathlib.Path.home() / "mymcp-workspace"))).resolve()

@mcp.tool(name="shell", description="Execute a shell command and return its output (truncated preview + full saved to file).")
def shell(command: Annotated[str, "Shell command to execute"], workdir: str = "", timeout: int = 120000) -> str:
    cwd = str(WS() / workdir) if workdir else str(WS())
    pathlib.Path(cwd).mkdir(parents=True, exist_ok=True)
    try:
        r = subprocess.run(command, shell=True, cwd=cwd, capture_output=True, text=True, timeout=timeout/1000)
        out = r.stdout + r.stderr
        if len(out) > 8000:
            return out[:8000] + f"\n... (truncated, {len(out)} bytes total, exit {r.returncode})"
        return out or f"(exit {r.returncode}, no output)"
    except subprocess.TimeoutExpired as e:
        return f"Timeout after {timeout}ms\n{(e.stdout or b'').decode()[:4000]}"

@mcp.tool(name="run_command", description="Alias for shell — execute a shell command.")
def run_command(command: str, workdir: str = "", timeout: int = 120000) -> str:
    return shell(command, workdir, timeout)

@mcp.tool(name="date_now", description="Current date/time (ISO 8601) and system info.")
def date_now() -> str:
    return datetime.datetime.now().isoformat()

@mcp.tool(name="list_env_vars", description="List environment variables.")
def list_env_vars() -> str:
    return "\n".join(f"{k}={v}" for k, v in sorted(os.environ.items()))

@mcp.tool(name="check_disk_space", description="Disk usage for the workspace filesystem.")
def check_disk_space() -> str:
    return subprocess.run(["df", "-h", str(WS())], capture_output=True, text=True).stdout

@mcp.tool(name="system_status", description="System status: load, memory, uptime.")
def system_status() -> str:
    out = []
    for cmd in [["uptime"], ["free", "-h"]]:
        try:
            out.append(subprocess.run(cmd, capture_output=True, text=True, timeout=5).stdout)
        except Exception as e:
            out.append(str(e))
    return "\n".join(out)

@mcp.tool(name="list_processes", description="List running processes (ps aux, truncated).")
def list_processes() -> str:
    r = subprocess.run(["ps", "aux", "--sort=-%cpu"], capture_output=True, text=True, timeout=5)
    lines = r.stdout.splitlines()
    return "\n".join(lines[:30])

@mcp.tool(name="grep", description="Search file contents with ripgrep (or grep fallback).")
def grep(pattern: str, path: str = ".", include: str = "") -> str:
    rg = shutil.which("rg") or "grep"
    cmd = [rg, "-n", pattern, path] if rg == "rg" else ["grep", "-rn", pattern, path]
    if include:
        cmd += ["--glob", include] if rg == "rg" else ["--include", include]
    r = subprocess.run(cmd, capture_output=True, text=True, timeout=15, cwd=str(WS()))
    return (r.stdout or r.stderr)[:8000] or "(no matches)"

@mcp.tool(name="find_files", description="Find files by glob pattern.")
def find_files(pattern: str, path: str = ".") -> str:
    import fnmatch
    base = WS() / path if not path.startswith("/") else pathlib.Path(path)
    hits = list(base.rglob(pattern)) if base.is_dir() else []
    return "\n".join(str(p) for p in hits[:100]) or "(no matches)"

@mcp.tool(name="count_lines", description="Count lines in a file.")
def count_lines(path: str) -> str:
    p = WS() / path if not path.startswith("/") else pathlib.Path(path)
    try:
        return str(sum(1 for _ in open(p)))
    except Exception as e:
        return str(e)

@mcp.tool(name="awk", description="Run awk on a file (via shell).")
def awk(pattern: str, path: str) -> str:
    return shell(f"awk '{pattern}' {path}")

@mcp.tool(name="sed", description="Run sed on a file (via shell).")
def sed(pattern: str, path: str) -> str:
    return shell(f"sed '{pattern}' {path}")

@mcp.tool(name="parse_json", description="Parse and pretty-print JSON from a file or string.")
def parse_json(content: str, path: str = "") -> str:
    try:
        if path:
            content = (WS() / path).read_text() if not path.startswith("/") else pathlib.Path(path).read_text()
        return json.dumps(json.loads(content), indent=2)
    except Exception as e:
        return f"Error: {e}"

@mcp.tool(name="parse_html", description="Parse HTML and extract text (requires lxml/html5lib).")
def parse_html(html: str = "", path: str = "", selector: str = "") -> str:
    try:
        from bs4 import BeautifulSoup
        src = (WS() / path).read_text() if path else html
        soup = BeautifulSoup(src, "html.parser")
        nodes = soup.select(selector) if selector else [soup]
        return "\n".join(n.get_text(strip=True) for n in nodes)[:8000]
    except Exception as e:
        return f"Error: {e}"

@mcp.tool(name="parse_css", description="Extract CSS rules matching a selector (best-effort).")
def parse_css(css: str = "", path: str = "", selector: str = "") -> str:
    try:
        src = (WS() / path).read_text() if path else css
        # minimal: return matching blocks
        return "\n".join(l for l in src.splitlines() if selector.lower() in l.lower())[:8000] or src[:8000]
    except Exception as e:
        return str(e)
