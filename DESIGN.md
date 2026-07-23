# mymcp — design

A small, self-contained **local MCP tool server**: it exposes host tools to any
MCP client over plain HTTP on loopback, gated by named bearer tokens. Pure Go
stdlib, no dependencies.

## Scope (and non-scope)

mymcp is **local-only** by design. It binds `127.0.0.1` and refuses a
non-loopback bind unless `--allow-remote` is passed. Network-level security
(TLS, mutual TLS, LAN exposure) is intentionally **not** mymcp's job — for that,
keep mymcp on loopback and put [`truemtls`](https://github.com/brywil/truemtls)
in front:

```
MCP client ──Bearer token──► truemtls (mandatory mTLS) ──► mymcp (loopback)
```

This is why an earlier mTLS-in-mymcp design was dropped: browser MCP clients
(e.g. the llama.cpp web UI) can't present client certificates, but they *can*
send an `Authorization` header — so a bearer token is the auth that actually
works across clients, and mTLS is layered on by truemtls only when needed.

## Transport

MCP Streamable HTTP: JSON-RPC 2.0 POSTed to `/mcp` (also `/`), answered
`application/json`. Handles `initialize`, `tools/list`, `tools/call`, `ping`,
and ignores notifications. Sets permissive CORS and answers OPTIONS preflight so
browser MCP clients work.

## Authentication: named bearer tokens

```
~/.config/mymcp/tokens/
  default          # file, 0600, contents = the secret token
  alice
  build-agent
```

- **Multiple tokens**, one file per name — hand-manageable, no database.
- **Issue:** `mymcp token add <name>` (or `mymcp token` for "default").
- **Revoke:** delete the file, or `mymcp token revoke <name>` — effective on the
  next request (the directory is re-read each time).
- **Authenticate:** the presented `Bearer` token is compared (constant-time)
  against every stored token; a match yields the token's **name** as the
  principal. Deny → `401`.
- **Logging:** every `tools/call` is logged as `principal=<name> tool=<name>`,
  so per-client attribution is in the server log.
- `--no-auth` disables the check (open; only sane on loopback).

serve auto-creates a `default` token on first run if the store is empty, so it's
never unintentionally open.

## Tools

Registered at boot into a workspace-confined registry: filesystem (read/write/
list/edit/…, confined to `--workspace`), `run_command`, git, http fetch, system
info. `tools/list` advertises them; `tools/call` dispatches.

> An allowed caller with `run_command`/write tools effectively has remote code
> execution — that is the intended power of the tool. The guardrails are the
> loopback bind, the workspace confinement for file tools, and the bearer token
> (plus truemtls in front for anything off-box).

## Packages

- `internal/mcp` — protocol + HTTP server (transport, CORS, bearer gate, dispatch).
- `internal/tokens` — the directory-backed named-token store.
- `internal/tools` — the tool catalog + workspace-confined registry.
- `cmd/mymcp` — the `serve` and `token` commands.

## Deploy

Per-user, no root: `~/.local/bin/mymcp`, a `systemd --user` unit, config/tokens
in `~/.config/mymcp/`. See the Taskfile (`build`/`test`/`deploy`/`install-unit`)
and README.
