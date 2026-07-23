# mymcp

A small, self-contained **local MCP tool server**. It exposes host tools
(file I/O, `run_command`, git, http, system info) to any MCP client over plain
HTTP on loopback, with an optional **bearer token**. Pure Go stdlib, no
dependencies.

- **Transport:** MCP Streamable HTTP (`/mcp`) — works with the llama.cpp web-UI
  MCP client and other HTTP MCP clients (which can send a custom
  `Authorization` header, so bearer auth "just works" where client certs don't).
- **Local by default:** binds `127.0.0.1` and refuses non-loopback binds unless
  you opt in with `--allow-remote`.
- **Auth:** named **bearer tokens** — one file per name in
  `~/.config/mymcp/tokens/`. Issue several (one per client/agent), **revoke** by
  deleting the file or `mymcp token revoke <name>`, and every request is
  **logged with the matching token's name**. (`--no-auth` for an open loopback
  server.)
- **For LAN / remote:** keep mymcp on loopback and front it with
  [`truemtls`](https://github.com/brywil/truemtls), which adds mandatory mutual
  TLS in front of any plain-HTTP backend:
  ```bash
  truemtls serve --backend http://127.0.0.1:9443 --listen 0.0.0.0:8443
  ```

> An allowed caller with `run_command`/write tools effectively has remote code
> execution — that is the point. The token + loopback bind are the guardrails;
> add truemtls for anything off-box. See [`DESIGN.md`](DESIGN.md).

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

## License

MIT.
