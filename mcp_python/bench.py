#!/usr/bin/env python3
"""Benchmark Go mymcp vs Python mcp_python mirror.

Raw MCP timing (p50/mean) + concurrent throughput. No LLM — just tool dispatch overhead.
Run from repo root:

    # Go must be running (default :9443 with bearer auth)
    mymcp serve --addr 127.0.0.1:9443 &
    # Python mirror (no-auth for bench parity, different port to avoid collision)
    uv run --with fastmcp python mcp_python/server.py --addr 127.0.0.1:9446 --no-auth &
    uv run --with fastmcp --with httpx python mcp_python/bench.py --json-out bench/results.json

Or as a one-shot:
    uv run python mcp_python/bench.py
    # bench.py will start Python on :9446 itself if not already running
"""

from __future__ import annotations

import argparse
import asyncio
import json
import statistics
import time
from pathlib import Path

import httpx

TOKENS_DIR = Path.home() / ".config" / "mymcp" / "tokens"


def _headers() -> dict:
    tok = (TOKENS_DIR / "default").read_text().strip() if (TOKENS_DIR / "default").exists() else ""
    h = {"Content-Type": "application/json", "Accept": "application/json, text/event-stream"}
    if tok:
        h["Authorization"] = f"Bearer {tok}"
    return h


async def _init_session(url: str, headers: dict) -> str:
    """MCP initialize handshake — returns mcp-session-id if the server issues one."""
    async with httpx.AsyncClient(timeout=10) as c:
        r = await c.post(url, json={
            "jsonrpc": "2.0", "id": 1, "method": "initialize",
            "params": {"protocolVersion": "2025-03-26", "capabilities": {}, "clientInfo": {"name": "bench", "version": "1.0"}}
        }, headers=headers)
        return r.headers.get("mcp-session-id", "")


async def bench_one(label: str, url: str, headers: dict, n: int = 15) -> dict:
    """Benchmark one server URL — returns a results dict."""
    from fastmcp import Client
    from fastmcp.client.transports import StreamableHttpTransport

    transport = StreamableHttpTransport(url, headers=headers)
    print(f"\n=== {label} @ {url} ===")
    # Suppress FastMCP's noisy disconnect BrokenResourceError (anyio buffer closed early) — results already collected
    from contextlib import suppress
    import anyio
    try:
        transport_client = Client(transport)
    except Exception:
        transport_client = None
    # Use contextlib to suppress disconnect errors
    from contextlib import asynccontextmanager
    @asynccontextmanager
    async def _client_ctx():
        async with Client(transport) as c:
            try:
                yield c
            except anyio.BrokenResourceError:
                pass
            except BaseException:
                pass
    async with _client_ctx() as client:
        # tools/list
        t0 = time.perf_counter()
        tools = await client.list_tools()
        list_ms = (time.perf_counter() - t0) * 1000
        print(f"  tools/list: {len(tools)} tools in {list_ms:.1f}ms")

        cases = [
            ("read_file", {"path": "README.md"}),
            ("shell", {"command": "echo hello"}),
            ("list_directory", {"path": "."}),
            ("grep", {"pattern": "mymcp", "path": "."}),
            ("web_fetch", {"url": "https://example.com"}),
        ]
        per_tool: dict = {}
        for name, args in cases:
            times = []
            for _ in range(n):
                t0 = time.perf_counter()
                try:
                    await client.call_tool(name, args)
                except Exception as e:
                    print(f"    {name} error: {e}")
                    times.append(9999)
                    continue
                times.append((time.perf_counter() - t0) * 1000)
            times.sort()
            per_tool[name] = {
                "p50_ms": round(times[len(times)//2], 1),
                "p95_ms": round(times[int(len(times)*0.95)], 1),
                "mean_ms": round(statistics.mean(times), 1),
                "raw": [round(x, 1) for x in times],
            }
            print(f"  {name:18s} p50={per_tool[name]['p50_ms']:5.1f}ms mean={per_tool[name]['mean_ms']:5.1f}ms")

        # concurrent throughput
        t0 = time.perf_counter()
        await asyncio.gather(*[client.call_tool("shell", {"command": "echo hi"}) for _ in range(20)])
        conc_ms = (time.perf_counter() - t0) * 1000
        print(f"  20× concurrent shell: {conc_ms:.1f}ms ({20/conc_ms*1000:.1f} req/s)")

        return {
            "label": label,
            "url": url,
            "n_tools": len(tools),
            "tools_list_ms": round(list_ms, 1),
            "per_tool": per_tool,
            "concurrent_20_ms": round(conc_ms, 1),
            "concurrent_tps": round(20 / (conc_ms/1000), 1),
        }


async def main():
    ap = argparse.ArgumentParser(description="Go vs Python mcp benchmark")
    ap.add_argument("--go-url", default="http://127.0.0.1:9443/mcp")
    ap.add_argument("--py-url", default="http://127.0.0.1:9446/mcp")
    ap.add_argument("--py-addr", default="127.0.0.1:9446", help="where to start Python mirror if not running")
    ap.add_argument("--n", type=int, default=15, help="calls per tool")
    ap.add_argument("--no-start-py", action="store_true", help="don't auto-start Python mirror")
    ap.add_argument("--json-out", default="", help="write JSON results")
    ap.add_argument("--md-out", default="", help="append markdown table to file")
    args = ap.parse_args()

    headers = _headers()
    py_headers: dict = {"Content-Type": "application/json", "Accept": "application/json, text/event-stream"}  # Python was --no-auth

    # Start Python mirror if needed
    py_proc = None
    if not args.no_start_py:
        import subprocess, sys
        import os
        # Check if something's already on the port
        try:
            async with httpx.AsyncClient(timeout=2) as c:
                await c.post(args.py_url, json={"jsonrpc":"2.0","id":1,"method":"tools/list"}, headers=py_headers)
                print(f"Python mirror already up on {args.py_url}")
        except Exception:
            print(f"Starting Python mirror on {args.py_addr} ...")
            py_proc = subprocess.Popen(
                [sys.executable, "mcp_python/server.py", "--addr", args.py_addr, "--no-auth"],
                cwd=str(Path(__file__).parent.parent),
                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
            )
            for _ in range(10):
                await asyncio.sleep(0.5)
                try:
                    async with httpx.AsyncClient(timeout=2) as c:
                        await c.post(args.py_url, json={"jsonrpc":"2.0","id":1,"method":"initialize",
                                                         "params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"bench","version":"1.0"}}},
                                     headers=py_headers)
                        break
                except Exception:
                    continue

    # Check Go is up
    try:
        import httpx as _hx
        async with _hx.AsyncClient(timeout=3) as c:
            await c.post(args.go_url, json={"jsonrpc":"2.0","id":1,"method":"initialize",
                                            "params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"bench","version":"1.0"}}},
                         headers=headers)
    except Exception as e:
        print(f"WARNING: Go server not responding on {args.go_url} ({e}) — is mymcp running?")

    go = await bench_one("Go  (build/mymcp)", args.go_url, headers, n=args.n)
    py = await bench_one("Python (mcp_python)", args.py_url, py_headers, n=args.n)

    # Summary table
    print("\n" + "="*78)
    print(f"{'':22s} {'Go p50':>10s} {'Python p50':>12s} {'Python/Go':>12s}")
    print("-"*78)
    print(f"{'tools/list':22s} {go['tools_list_ms']:>8.1f}ms {py['tools_list_ms']:>10.1f}ms {py['tools_list_ms']/max(go['tools_list_ms'],0.1):>10.1f}x")
    for name in go["per_tool"]:
        if name in py["per_tool"]:
            g = go["per_tool"][name]["p50_ms"]
            p = py["per_tool"][name]["p50_ms"]
            print(f"{name:22s} {g:>8.1f}ms {p:>10.1f}ms {p/max(g,0.1):>10.1f}x")
    print(f"{'20× concurrent':22s} {go['concurrent_20_ms']:>8.1f}ms {py['concurrent_20_ms']:>10.1f}ms {py['concurrent_20_ms']/max(go['concurrent_20_ms'],0.1):>10.1f}x")
    print("-"*78)
    print(f"Go: {go['n_tools']} tools | Python: {py['n_tools']} tools")
    print(f"Concurrent throughput: Go {go['concurrent_tps']}/s  Python {py['concurrent_tps']}/s")
    print("="*78)

    if args.json_out:
        Path(args.json_out).parent.mkdir(parents=True, exist_ok=True)
        Path(args.json_out).write_text(json.dumps({"go": go, "python": py}, indent=2))
        print(f"\nWrote {args.json_out}")

    if py_proc and not args.no_start_py:
        py_proc.terminate()
        try:
            py_proc.wait(timeout=5)
        except Exception:
            py_proc.kill()

if __name__ == "__main__":
    asyncio.run(main())
