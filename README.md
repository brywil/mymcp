# mymcp

A small, self-contained **local MCP tool server**. It exposes a full host-tool
catalog (~79 tools: file I/O, git, `gh`, HTTP/parse, Ollama web search, system
utilities, and tmux/`run_command`) to any MCP client over plain HTTP on loopback,
with an optional **bearer token**. Go stdlib plus a single dependency
(`golang.org/x/net/html`, for the `parse_html`/`parse_css` tools).

> **New to MCP?** Start with [What is MCP? Where does mymcp fit?](docs/WHAT_IS_MCP.md)
> — it explains LLMs, harnesses (goclaw, Claude Code, etc.), and MCP clients/servers
> without assuming prior knowledge.

This is goclaw's host-tool backend: goclaw mounts these tools over MCP under
their bare names, so they replace what goclaw used to register natively. Tools
that bind to goclaw's live process (conversation/context, cron, Telegram,
opencode/claude, headless-browser rendering) stay native in goclaw.

## How it fits together

```
LLM (the model)  ←→  Harness (the agent runtime)  ←─MCP──→  mymcp / myagent
                      goclaw, Claude Code,                    (tool servers)
                      OpenCode, llama.cpp UI, …                on loopback
```

- **LLM** — a text-prediction engine — e.g. GPT-4o/GPT-5 (behind ChatGPT),
  Claude Sonnet/Opus, Gemini, Llama, Qwen. It has no files, shell, or browser.
- **Harness** — the program that wraps the LLM and talks to tool servers — e.g.
  ChatGPT, Codex, Claude Code/Desktop, Cursor, OpenCode, goclaw. It keeps the
  conversation, sends tool definitions to the model, and dispatches the model's
  tool calls over MCP. It is the **MCP client**.
- **MCP** (Model Context Protocol) — the open standard (JSON-RPC over HTTP or
  stdio) that lets any harness talk to any tool server.
- **mymcp** — the **MCP server** in this repo: ~79 host tools (files, shell, git,
  web, memory, …) on `127.0.0.1:9443/mcp`. Compare community MCP servers for GitHub,
  Slack, Postgres, etc. — same protocol, different tools. See
  [What is MCP?](docs/WHAT_IS_MCP.md) for the full picture, including **myagent**
  (agent-to-agent messaging on `:9444`).

## At a glance (this server)

- **Transport:** Streamable HTTP (`/mcp`) — works with the llama.cpp web-UI
  MCP client and other HTTP MCP clients (which can send a custom
  `Authorization` header, so bearer auth "just works" where client certs don't).
  Most community MCP servers use stdio instead; mymcp uses HTTP so multiple
  clients can share one instance.
- **Local by default:** binds `127.0.0.1` and refuses non-loopback binds unless
  you opt in with `--allow-remote`.
- **Auth:** named **bearer tokens** (this server's choice — not part of generic
  MCP) — one file per name in `~/.config/mymcp/tokens/`. Issue several (one per
  client/agent), **revoke** by deleting the file or `mymcp token revoke <name>`,
  and every request is **logged with the matching token's name**. (`--no-auth`
  for an open loopback server.)
- **For LAN / remote:** keep mymcp on loopback and front it with
  [`truemtls`](https://github.com/brywil/truemtls), which adds mandatory mutual
  TLS in front of any plain-HTTP backend:
  ```bash
  truemtls serve --backend http://127.0.0.1:9443 --listen 0.0.0.0:8443
  ```

> An allowed caller with `run_command`/write tools effectively has remote code
> execution — that is the point. The token + loopback bind are the guardrails;
> add truemtls for anything off-box. These are choices this server makes; the
> MCP spec itself doesn't mandate them. See [`DESIGN.md`](DESIGN.md).

## Prerequisites: a working Go environment

> If you don't do Go development day to day, read this first — it's the #1
> reason a freshly `go install`ed tool reports "command not found".

`go install` writes binaries to `$GOBIN` (or `$GOPATH/bin`). If that isn't on
your `PATH`, installed binaries exist but your shell can't find them. Set up once
in `~/.bashrc`:

```bash
export GOROOT=/usr/local/go            # the Go toolchain (provides `go`)
export GOPATH="$HOME/go"               # your Go workspace
export GOBIN="$GOPATH/bin"             # where `go install` puts binaries
export PATH="$GOROOT/bin:$GOPATH/bin:$HOME/.local/bin:$PATH"
```

Then `source ~/.bashrc`. Install [go-task](https://taskfile.dev/) in one line:

```bash
sudo sh -c "$(curl -sL https://taskfile.dev/install.sh)" -- -d -b /usr/local/bin
```

## Build, run, deploy

```bash
task build            # -> build/mymcp
task test             # go test ./...
task install          # -> ~/.local/bin/mymcp
task install-unit     # systemd --user service (no sudo); enables but doesn't start
# edit ~/.config/mymcp/mymcp.env (ADDR, WORKSPACE), then:
systemctl --user start mymcp
task deploy           # build + test + stop + install + start (atomic, no sudo)
```

Everything is per-user: binary in `~/.local/bin`, unit in
`~/.config/systemd/user/`, config in `~/.config/mymcp/`. No root.

## Connecting a client

```bash
mymcp token               # prints the "default" token (creates it on first run)
mymcp token add alice     # or mint a named token per client/agent
```

Point your MCP client at `http://127.0.0.1:9443/mcp` and set the header
`Authorization: Bearer <that token>`. Each client can have its own token, so the
server log shows exactly who called what, and you can revoke one without
touching the others:

```bash
mymcp token list          # alice, build-agent, default, …
mymcp token revoke alice  # alice's token stops working immediately
```

## Commands

| Command | Purpose |
|---|---|
| `mymcp serve [--addr --workspace --no-auth --allow-remote]` | run the tool server |
| `mymcp token` | print/create the "default" token |
| `mymcp token add\|show\|revoke <name>` / `list` | manage named tokens |

## Further reading

- [What is MCP? Where does mymcp fit?](docs/WHAT_IS_MCP.md) — LLMs, harnesses, MCP, and where mymcp/myagent sit.
- [DESIGN.md](DESIGN.md) — scope, transport, auth, and package layout.

## License

MIT.
