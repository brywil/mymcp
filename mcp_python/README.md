# fastmcp — Python mirror of mymcp

This directory is a **FastMCP mirror** of the Go `mymcp` server. Same tool catalog (~81 tools), same Streamable HTTP transport (`/mcp` on `127.0.0.1:9443`), same named bearer-token auth (`~/.config/mymcp/tokens/*`). It exists to benchmark Python/FastMCP against the Go implementation on the same harness.

```
Go:      cmd/mymcp + internal/{server,mcp,tokens,tools}  →  build/mymcp  (single binary, 1 dep)
Python:  mcp_python/server.py + mcp-python/tools/*.py            →  uv run mcp_python/server.py  (FastMCP 4.1 + stdlib)
```

## Run

```bash
# From repo root — uses the same token files the Go binary mints
uv run python mcp_python/server.py                          # bearer auth (default)
uv run python mcp_python/server.py --no-auth                # open loopback (bench parity)
uv run python mcp_python/server.py --addr 127.0.0.1:9444    # myagent-side port

# Or directly
python mcp_python/server.py --help
```

Point a client at `http://127.0.0.1:9443/mcp` with `Authorization: Bearer <token from mymcp token>`.

## Layout

| File | Mirrors |
|---|---|
| `server.py` | `cmd/mymcp` + `internal/mcp` + `internal/tokens` — arg parsing, loopback guard, `NamedBearerAuth` |
| `tools/_registry.py` | `internal/tools/registry.go` — shared `FastMCP` instance |
| `tools/fs_tools.py` | `internal/tools/fs.go` (13 tools) |
| `tools/shell_tools.py` | `internal/tools/system.go` + `presets.go` + `sanitize.go` (shell, grep, processes, etc.) |
| `tools/git_tools.py` | `internal/tools/git.go` + `gh.go` (git + 14 `gh` tools) |
| `tools/web_tools.py` | `internal/tools/web.go` + `websearch_free.go` |
| `tools/system_tools.py` | `internal/tools/system.go` (system/tmux/memory/recall/thinking stubs) |
| `tools/tmux_tools.py` | `internal/tools/tmux.go` + `wait.go` |
| `tools/misc_tools.py` | `pubsub.go`, `recall.go`, `myagent` stubs — ~81 tools total |

## Go vs FastMCP

|  | Go (`mymcp`) | Python (`mcp_python/`) |
|---|---|---|
| Binary | `build/mymcp` (~8 MB static) | `fastmcp` + Python runtime |
| Deps | `x/net/html` only | `fastmcp`, `httpx`, `bs4/lxml` |
| Schema | hand-written JSON Schema per tool | auto-generated from type hints |
| Perf | ~0.5 ms/tool dispatch, no GC pauses | FastMCP overhead + Python GIL |
| Auth | `NamedBearerAuth` over `~/.config/mymcp/tokens/` | Same files, `BearerAuthProvider` subclass |
| Transport | `net/http` Streamable HTTP | Starlette/Uvicorn via FastMCP |

The Go server is the production path; this mirror is for comparison and for callers who prefer Python tooling.

## Benchmarking

> `quantkit/bench/run_bench.sh` **does not use mymcp** — it drives OpenCode against `llama-server` directly (different harness). To compare the two tool servers, use **goclaw** as the harness (it mounts `mymcp` over MCP) or the micro-bench below.

**goclaw path** (apples-to-apples agentic run, same LLM + task set, different tool server):

```bash
# Go (production)
mymcp serve --addr 127.0.0.1:9443 &

# Python mirror — different port to avoid collision
uv run python mcp_python/server.py --addr 127.0.0.1:9444 --no-auth &
# Point goclaw at :9444 for the Python arm, :9443 for the Go arm — same model, same task set
```

### Micro-bench (tool dispatch, no LLM — `mcp_python/bench.py`)

| tool | Go p50 | Python p50 |
|---|---:|---:|
| `read_file` | 0.4 ms | 3.6 ms |
| `shell` (`echo hello`) | 0.4 ms | 4.4 ms |
| `list_directory` | 1.7 ms | 5.5 ms |
| `grep` | 986 ms | — |
| `web_fetch` | 288 ms | — |
| 20× concurrent `shell` | 33.5 ms (597 req/s) | ~79 ms (253 req/s) |

Run it yourself:

```bash
uv run python mcp_python/bench.py --n 15                  # auto-starts Python on :9446
uv run python mcp_python/bench.py --n 15 --json-out bench.json  # + JSON
```

## Notes

- Tool errors are returned as descriptive strings (e.g. `"old_string not found in ..."`) — matching the Go `Handler` convention, not MCP `isError` envelopes. This is intentional for harness parity.
- Browser tools (`screenshot`, `evaluate`, `navigate`) are stubs unless a real browser session is attached — same as the Go server without a browser.
- `myagent` (agent-to-agent pubsub on `:9444`) is not reimplemented here; use the Go binary for that.
