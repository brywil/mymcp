# mymcp — design

A small, self-contained MCP server that exposes host tools (file I/O, exec, git,
http, …) to any MCP-speaking agent, with mandatory mTLS, a hand-manageable
directory trust model, and per-principal capability control. Pure Go stdlib, no
dependencies.

## Components (one binary, subcommands)

- `mymcp serve` — the tool server. Streamable-HTTP MCP over **mandatory mTLS**.
- `mymcp connect` — client-side sidecar: listens on localhost plain HTTP,
  originates mTLS upstream (holds the client cert). Lets clients that can't do
  mTLS (most MCP clients, incl. the llama.cpp web UI) connect with zero mTLS
  support. Hardware-token (PKCS#11) cert source is an optional build-tagged
  extension; software PEM certs are the stdlib default.
- `mymcp trust` — manage trusted authorities / pins / pending, out of band.
- `mymcp principal` — manage the capability database (who can call what).
- `mymcp certs` — optional PKI generator for users without an existing CA.

## Transport

MCP Streamable HTTP: JSON-RPC 2.0 POSTed to `/mcp`, answered `application/json`.
The server sets permissive CORS so a browser MCP client connects **directly** —
the llama.cpp `--webui-mcp-proxy` stays **off**, sidestepping its auth-header
bugs (#21012, #21167).

## Identity model: authenticate by CA, authorize by CN

- **Authentication (who signed you): mTLS.** `crypto/tls` with
  `ClientAuth: RequireAnyClientCert` + a custom `VerifyPeerCertificate`. A client
  cert is authenticated iff it is a pinned leaf, or it chains to a CA in
  `authorities/`. An org may have **several** CAs (CAC/PIV rotation, multiple
  issuers) — all are equal authentication anchors.
- **Authorization (what you may do): by CN principal.** The client cert's
  Subject **CommonName** is the principal. Authorization is keyed on the CN and
  is **independent of which CA issued the cert**, so re-issuing a user's token
  under a different org CA needs no re-registration. A principal must exist in
  the capability database and the specific tool must be in its allow-set.

> Trust assumption: because a CN is honored regardless of issuing CA, only
> **org-controlled** CAs belong in `authorities/` — any of them can assert a CN.

## Trust store — a directory tree (authentication)

```
<trust-root>/                 # e.g. ~/.config/mymcp/trust
  authorities/                # trusted CA certs (one PEM per CA)
  pinned/                     # exact leaf certs (self-authenticating, no CA)
  pending/                    # unknown certs captured at handshake, awaiting approval
```

Trust a CA = drop its PEM in `authorities/`; revoke = delete it. On an unknown
cert the handshake is rejected and the chain written to `pending/`. Approve:

- `mymcp trust approve authority <fp>` — trust the issuing CA (adds to `authorities/`).
- `mymcp trust pin <fp>` — exact-leaf trust (adds to `pinned/`), for ad-hoc/no-CA clients.
- `mymcp trust list` / `revoke` — inspect / remove.

## Capability database — per principal (authorization)

```
<config>/principals/          # e.g. ~/.config/mymcp/principals
  <slug>.principal            # one file per principal
```

Each `.principal` file (hand-editable):

```
cn = Alice Example            # exact CN to match (may contain spaces)
allow = read_file, list_directory, path_info, git_status
# allow = *                   # all tools
```

Enforcement:

- On connect, the peer CN must resolve to a known principal, else `403` (and the
  attempt is logged for registration).
- On `tools/call`, the tool name must be in that principal's allow-set (or `*`).

CLI: `mymcp principal add "<cn>" --allow read_file,list_directory`,
`mymcp principal list`, `mymcp principal allow "<cn>" run_command`,
`mymcp principal rm "<cn>"`. Registering from a pending cert:
`mymcp principal add-pending <fp> --allow …` (reads the CN from the queued cert).

Default is **deny**: a brand-new principal with no `allow` entries can call
nothing until granted.

## Tools

Full host tool set, each call gated by the principal's allow-set: filesystem
(confined to a `--workspace` root), `run_command`, git, http fetch, system info.
An allowed principal with `run_command`/write tools effectively has RCE — which
is why authn is mandatory and authz is deny-by-default and per-tool.

## Factoring: standalone mTLS gate (planned)

The trust/policy/pki core + CLI verbs are independent of MCP and are the reusable
asset — they remove most of the pain of server-side mTLS administration. Plan to
extract them into their own module (`truemtls` — mutual TLS done properly, minus
the operational tax), exposed as:

1. a **Go library** any server imports (install the store's `Verify` as
   `tls.Config.VerifyPeerCertificate`, reuse the `trust`/`allow`/`principal` CLIs);
2. a **standalone reverse-proxy** (`truemtls serve --backend http://…`) that fronts
   any HTTP service and adds mTLS + directory trust + TOFU + CN authz with zero
   backend changes.

`mymcp` becomes the first consumer; `mymcp connect` is the client-side counterpart.

## Install (`install.sh`) — XDG + user service

No root. Binary → `~/.local/bin/mymcp`. Trust root →
`~/.config/mymcp/trust/{authorities,pinned,pending}`. Principals →
`~/.config/mymcp/principals/`. Server identity → `~/.config/mymcp/server.{crt,key}`
(self-signed if absent). Config → `~/.config/mymcp/config.env`. State/logs →
`~/.local/state/mymcp/`. **User systemd unit** →
`~/.config/systemd/user/mymcp.service`, `systemctl --user enable --now mymcp`
(+ `loginctl enable-linger` to survive logout). `--purge` also removes the trust
root and principals.
