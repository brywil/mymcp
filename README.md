# mymcp

A small, self-contained **MCP tool server** that exposes host tools (file I/O,
`run_command`, git, http, system info) to any MCP-speaking agent — over
**mandatory mutual TLS**, with **per-principal (certificate CN) capabilities**.
Pure Go stdlib plus [`truemtls`](https://github.com/brywil/truemtls) for the
mTLS trust layer.

- **Transport:** MCP Streamable HTTP (`/mcp`), so it works with the llama.cpp
  web-UI MCP client and other HTTP MCP clients.
- **Authentication:** mandatory mTLS via `truemtls` — a client cert must be
  pinned or chain to a trusted CA (a directory of PEM files you manage by hand).
- **Authorization:** each cert CN is a principal; **deny-by-default**, granted
  per tool (`ro` / `all` / individual tools). Enforced on every `tools/call`,
  and `tools/list` is filtered to what the principal may call.

> An allowed principal with `run_command`/write tools effectively has remote
> code execution — which is the point, and why auth is mandatory and authz is
> deny-by-default. See [`DESIGN.md`](DESIGN.md) for the full model.

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

Then `source ~/.bashrc`. Install [go-task](https://taskfile.dev/) in one line
(to `/usr/local/bin`, always on `PATH`):

```bash
sudo sh -c "$(curl -sL https://taskfile.dev/install.sh)" -- -d -b /usr/local/bin
```

## Build, run, deploy

```bash
task build            # -> build/mymcp
task test             # go test ./...
task install          # -> ~/.local/bin/mymcp
task install-unit     # systemd --user service (no sudo); enables but doesn't start
# edit ~/.config/mymcp/mymcp.env (WORKSPACE, ADDR), then:
systemctl --user start mymcp
task deploy           # build + test + stop + install + start (atomic, no sudo)
```

Everything is per-user: binary in `~/.local/bin`, unit in
`~/.config/systemd/user/`, config/trust in `~/.config/mymcp/`. No root.

## Onboarding a client

```bash
# trust the client's CA (drop its PEM in — hand-managed), or pin one cert:
cp your-org-ca.pem ~/.config/mymcp/trust/authorities/
# grant the principal (the client cert's CN) some capabilities:
mymcp allow "Alice Example" ro       # ro = all read-only tools
mymcp allow "build-agent"    all     # everything
```

Inspect/approve: `mymcp trust list` / `mymcp trust approve authority <fp>` /
`mymcp trust pin <fp>`. List principals: `mymcp principal list`.

## Commands

| Command | Purpose |
|---|---|
| `mymcp serve` | run the tool server (mandatory mTLS) |
| `mymcp trust list \| approve authority <fp> \| pin <fp>` | manage the mTLS trust store |
| `mymcp allow <cn> <tool\|ro\|all>` | grant a capability to a principal |
| `mymcp principal list \| rm <cn>` | manage principals |

## License

MIT.
