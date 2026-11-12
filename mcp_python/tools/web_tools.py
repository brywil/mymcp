"""Web tools — mirror of internal/tools/web.go + websearch_free.go."""

import os, subprocess
from typing import Annotated
from mcp_python.tools._registry import mcp

@mcp.tool(name="web_fetch", description="Fetch a URL and return markdown/text (follows redirects).")
def web_fetch(url: Annotated[str, "URL to fetch"], format: str = "markdown") -> str:
    # Use curl + html-to-text fallback; mirrors Go's webfetch behavior
    r = subprocess.run(["curl", "-sL", "--max-time", "30", "-A", "mymcp/1.0", url],
                       capture_output=True, text=True, timeout=35)
    body = r.stdout
    if format == "text":
        return body[:15000]
    # crude markdown: strip tags if it looks like HTML
    if "<html" in body.lower() or "<body" in body.lower():
        try:
            from bs4 import BeautifulSoup
            soup = BeautifulSoup(body, "html.parser")
            return soup.get_text("\n", strip=True)[:15000]
        except Exception:
            pass
    return body[:15000] or f"(empty, status {r.returncode})"

@mcp.tool(name="web_search_free", description="Free web search (DuckDuckGo HTML scrape, no API key).")
def web_search_free(query: Annotated[str, "Search query"]) -> str:
    r = subprocess.run(["curl", "-s", f"https://html.duckduckgo.com/html/?q={query}"],
                       capture_output=True, text=True, timeout=15)
    try:
        from bs4 import BeautifulSoup
        soup = BeautifulSoup(r.stdout, "html.parser")
        hits = []
        for a in soup.select("a.result__a")[:8]:
            hits.append(f"- [{a.get_text(strip=True)}]({a.get('href')})")
        return "\n".join(hits) or r.stdout[:4000]
    except Exception:
        return r.stdout[:4000]

@mcp.tool(name="web_search_paid", description="Placeholder for paid search (Tavily/SerpAPI) — set TAVILY_API_KEY to enable.")
def web_search_paid(query: str) -> str:
    key = os.environ.get("TAVILY_API_KEY", "")
    if not key:
        return "web_search_paid not configured (set TAVILY_API_KEY). Falling back to web_search_free — call that instead."
    import subprocess, json
    r = subprocess.run(["curl","-s","https://api.tavily.com/search","-H","Content-Type: application/json","-d", json.dumps({"api_key":key,"query":query,"max_results":5})],
                       capture_output=True, text=True, timeout=10)
    return r.stdout[:8000]

@mcp.tool(name="http_request", description="Raw HTTP request (method, url, headers, body).")
def http_request(url: str, method: str = "GET", headers: str = "", body: str = "") -> str:
    import subprocess, shlex
    cmd = ["curl", "-s", "-X", method, url, "--max-time", "20"]
    if headers:
        for h in headers.splitlines():
            if h.strip():
                cmd += ["-H", h.strip()]
    if body:
        cmd += ["-d", body]
    r = subprocess.run(cmd, capture_output=True, text=True, timeout=25)
    return (r.stdout + r.stderr)[:12000]

@mcp.tool(name="web_inspect", description="Fetch a URL and return headers + truncated body for debugging.")
def web_inspect(url: str) -> str:
    r = subprocess.run(["curl", "-sI", "--max-time", "10", url], capture_output=True, text=True, timeout=12)
    return r.stdout[:4000] or r.stderr[:2000]

