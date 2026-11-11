# What is MCP? Where does mymcp fit?

A short guide for new users. No prior MCP experience assumed.

> **Reading tip:** Sections 1–3 describe **all MCP** — the protocol itself. Sections 4–6 describe **this MCP server** (mymcp/myagent) specifically. Callouts like `MCP` vs `mymcp` make the split explicit.

## The 30-second version

- An **LLM** (large language model) is a text-prediction engine — e.g. GPT-4o / GPT-5 (behind ChatGPT), Claude Sonnet/Opus, Gemini, Llama, Qwen. It can reason, write, and answer questions, but on its own it cannot read your files, run commands, or browse the web.
- A **harness** (also called an agent harness / agent runtime) is the program that wraps the LLM and lets it *do* things: it manages the conversation, calls tools, and feeds results back to the model. E.g. ChatGPT, Claude Code / Claude Desktop, Cursor, **Codex** (OpenAI), OpenCode, goclaw.
- **MCP** (Model Context Protocol) is the open standard that connects the two sides: harnesses (MCP **clients**) talk to tool providers (MCP **servers**) over a shared JSON-RPC protocol.

```
User  →  Harness (client)  ⇄  MCP  ⇄  Tool server  →  Your machine
          (goclaw, Claude Code,            (mymcp, myagent, ...)
           OpenCode, etc.)                  reads files, runs shell,
                                            searches memory, ...
                 ↕
                LLM (the model — local via llama.cpp, or hosted)
```

**mymcp** is one such tool server (local, host-tool focused). **myagent** is its companion for agent-to-agent messaging. Sections 1–3 cover the protocol in general; §4 onward cover this server's choices.

---

## 1. The three actors (all MCP)

### The LLM — the brain, nothing else

An LLM takes text in and produces text out. It has no filesystem, no shell, no browser. It can be:

- **Hosted** — an API like OpenAI's GPT-4o/GPT-5 (what powers ChatGPT), Anthropic's Claude Sonnet/Opus, or Google's Gemini.
- **Local** — a model running on your machine via `llama.cpp`, Ollama, vLLM, etc. (e.g. Llama 3, Qwen, Mistral).

> **Note:** "ChatGPT" is not itself an LLM — it is OpenAI's *harness* wrapped around a GPT model. Same for "Claude" the app vs. the Claude model underneath. People conflate them because the product and the model share a name, but architecturally they are separate: the harness is the product you talk to, the LLM is the engine it calls.

The harness decides which model to use and can switch between them. The model itself never opens a network socket to a tool server.

### The harness — the hands and memory

A harness is the long-running program the user actually interacts with. Examples:

- **ChatGPT** (OpenAI), **Claude Desktop / Claude Code** (Anthropic), **Cursor**, **Codex** (OpenAI's terminal/cloud agent), **OpenCode**, **goclaw**, the **llama.cpp web UI**, or a Telegram bridge.

Each pairs with one or more LLMs — e.g. Codex and ChatGPT call GPT models, Claude Code calls a Claude model, goclaw can call a local Llama via llama.cpp *or* a hosted Claude/GPT, OpenCode can swap models freely.

It does:

1. Keeps the conversation history and system prompt.
2. Sends context + tool definitions to the LLM.
3. Receives the LLM's decision ("call `read_file` with `path: README.md`").
4. Dispatches that call to the right MCP server.
5. Returns the tool's output to the LLM so it can continue.
6. Repeats until the answer is ready, then shows it to you.

Think of the harness as an event loop around the model. Without one, an LLM is a chatbot in a box. With one, it becomes an agent.

### MCP — the plug (all MCP)

MCP (by Anthropic, now an open standard) says: harnesses don't need to reimplement every tool, and tool servers don't need to know which harness calls them. They agree on:

- **Transport** — how bytes move (stdio or Streamable HTTP — see §3).
- **Messages** — JSON-RPC 2.0 methods: `initialize`, `tools/list`, `tools/call`, `ping`, plus notifications.
- **Tool definitions** — each tool has a `name`, `description`, and JSON Schema for its arguments.

Any MCP client can talk to any MCP server that speaks the same transport. That is the whole point.

---

## 2. Client vs. server — who is which? (all MCP)

The names are from the harness's perspective and they confuse everyone once.

| Role | What it does | Example |
|------|-------------|---------|
| **MCP client** | Lives *inside* the harness. Discovers tools, forwards calls. | goclaw's built-in MCP client, Codex's MCP support, Claude Code's MCP connector, OpenCode's MCP support |
| **MCP server** | Exposes tools over MCP. Knows nothing about the LLM. | `mymcp` (files/shell/git), `myagent` (agent messaging), or community servers (GitHub, Slack, Postgres, Puppeteer, …) |

Generically, the flow is:

- You configure the harness with "connect to this MCP server."
- At startup, the harness (client) sends `initialize` → server answers with its name/version.
- Harness sends `tools/list` → server returns its tool definitions.
- When the model wants a tool, the harness sends `tools/call` → server executes it and returns text.

The MCP **server** never contacts the LLM. The **LLM** never contacts the MCP server. The harness sits in the middle and shuttles messages both ways.

> **This server's concrete example:** You configure goclaw with `http://127.0.0.1:9443/mcp` + a bearer token. goclaw sends `initialize` → mymcp answers; `tools/list` → mymcp returns ~79 tools; `tools/call` for `read_file` → mymcp reads the file.

---

## 3. Transport — how they talk (all MCP)

MCP defines two main transports — the spec allows either, and harnesses/servers pick one:

- **stdio** — the harness spawns the server as a child process and talks over stdin/stdout. Simple, but one server per harness, no sharing. Common for single-user desktop setups.
- **Streamable HTTP** — the server listens on an HTTP endpoint (`POST /mcp`). Any number of harnesses (or browser clients) can connect. Natural for shared or networked servers.

```
stdio:       harness ──spawn──► server process ──► local resources
                          stdin/stdout only

HTTP:        browser ─┐
             harness ──┼──► http://host:port/mcp ──► server's resources
             harness ─┘
```

Most community servers use stdio. mymcp specifically uses Streamable HTTP — which is why it can serve goclaw, the llama.cpp web UI, and others at the same time, and why it needs its own auth (see §5).

---

## 4. This server: where mymcp and myagent fit

> Everything in this section is **specific to this repo**, not generic MCP.

### mymcp — the host-tool server

**What it is:** A single Go binary that exposes a full host-tool catalog (~79 tools) over Streamable HTTP on loopback (`127.0.0.1:9443/mcp`). Go stdlib + one dependency (`golang.org/x/net/html`).

**What it provides (mymcp-only):** File I/O (confined to `--workspace`), `run_command`, `git`/`gh`, HTTP/fetch/HTML parsing, web search, web inspection via a headless browser (CDP on `127.0.0.1:9222`), `tmux` control, per-agent memory (`MEMORY.md`), conversation recall, `wait_for`, `list_tools`, and more. Other MCP servers expose completely different tools — e.g. a GitHub MCP server exposes `create_issue`, a Postgres server exposes `query`.

**What it is not:** It is not a harness and not an LLM. It does not keep conversation history, pick a model, or render a UI. It is the "do things on this machine" backend that a harness mounts over MCP under bare tool names. Tools that must bind to the harness's live process (cron, Telegram, live conversation context, `opencode`/`claude` subprocesses) intentionally stay native in the harness — see `DESIGN.md`.

**How it is configured (mymcp-only):**

```
mymcp serve --addr 127.0.0.1:9443 --workspace ~/mymcp-workspace
            --tools all          # or e.g. "ro,memory_save" to whitelist
            --allow-exec         # gate for shell-backed tools
```

### myagent — the messaging server (mymcp-only)

**What it is:** A second, separate MCP server in this repo (same auth, different port `127.0.0.1:9444`). It does one thing: let agents talk to each other. This is not part of generic MCP — it is an extra service this repo provides.

**Tools:** `agent_publish`, `agent_subscribe`, `agent_unsubscribe`, `agent_poll`, `agent_list`.

**Why separate from mymcp:**

- **Different failure domain** — the message store may live on a shared mount; if it stalls it must not take `read_file` down with it.
- **Different grant** — an agent can be given messaging *without* filesystem or shell access (useful before letting a cloud model join).
- **Different scope** — `mymcp` means "do this on *this* machine"; messaging is explicitly cross-machine.

Remote agents don't need a shared filesystem — they all connect to the one `myagent` server over mTLS (via `truemtls`), and the server keeps the store on its own disk. Every participant dials *in*; the server never dials out.

---

## 5. Security model (this server — not generic MCP)

> The MCP spec defines transports and message shapes. It does **not** mandate authentication, workspace confinement, or tool whitelisting. All of the below are **choices this server makes**. Other MCP servers (especially stdio ones) rely on OS process isolation instead.

An allowed caller with `run_command` / write tools effectively has remote code execution — that is the point. This server's guardrails are:

1. **Loopback by default (mymcp-only)** — binds `127.0.0.1` and refuses non-loopback addrs unless `--allow-remote` is passed. LAN/remote access is done by fronting with [`truemtls`](https://github.com/brywil/truemtls), which adds mandatory mutual TLS in front of any plain-HTTP backend:
   ```
   MCP client ──Bearer──► truemtls (mTLS) ──► mymcp (loopback, plain HTTP)
   ```
2. **Named bearer tokens (mymcp/myagent-only)** — one file per name in `~/.config/mymcp/tokens/` (e.g. `default`, `alice`, `build-agent`). Issue with `mymcp token add <name>`, revoke by deleting the file or `mymcp token revoke <name>`. Every `tools/call` is logged as `principal=<name> tool=<name>`. The directory is re-read each request, so revocation is immediate. `myagent` shares this token store — the token name *is* the agent identity.
3. **Workspace confinement (mymcp-only)** — file tools cannot escape `--workspace`.
4. **`--tools` whitelist (mymcp-only)** — enforced on dispatch, not just on `tools/list` (listing-only filtering would be security theatre; a model that has seen a tool name can call it). `read_only` preset `ro` available.
5. **`--allow-exec` gate (mymcp-only)** — disables all shell-backed tools (`run_command`, `git`, `gh`, `tmux`) when you want a read-only instance.

---

## 6. A complete request in slow motion (generic flow, mymcp example)

The generic MCP flow, with this server's values filled in:

1. You say to your harness (e.g. goclaw): "What's in README.md?"
2. Harness sends your message + tool definitions (from the server's earlier `tools/list`) to the LLM.
3. The LLM returns a tool call: `{ "name": "read_file", "arguments": { "path": "README.md" } }`.
4. Harness forwards it as `tools/call` to the MCP server — in this repo's case `http://127.0.0.1:9443/mcp` with `Authorization: Bearer <token>`. A stdio server would receive the same JSON over stdin.
5. Server executes the tool — mymcp authenticates the token → principal `alice`, checks the whitelist, reads `~/mymcp-workspace/README.md`, returns the text.
6. Harness appends the tool result to the conversation and sends it back to the LLM.
7. The LLM produces the final answer; the harness shows it to you.

The model never saw your filesystem. The tool server never saw the model. The harness orchestrated everything.

---

## 7. Common misconceptions (all MCP)

**"MCP is just tool calling."** Tool calling is the mechanism LLMs use to request tools; MCP is the protocol harnesses and tool servers use to *provide* them. You can do tool calling without MCP (hardcode tools in the harness), but then every harness reimplements every tool. MCP is the shared plug.

**"The LLM talks to the MCP server."** No. The harness talks to both sides. The LLM emits a structured request; the harness translates it into an MCP `tools/call`. The response flows back the same way.

**"stdio vs HTTP — which is 'real' MCP?"** Both are. stdio is simpler for one-harness-one-server setups. HTTP is for shared servers, browser clients, and multi-tenant setups (mymcp uses HTTP for this reason). They speak the same JSON-RPC; only the transport differs.

**"Why not put all tools in the harness?"** You can, and many harnesses do for process-bound tools. But host tools (files, shell, git) are independent of any harness — extracting them into `mymcp` means one implementation shared by goclaw, Codex, Claude Code, the llama.cpp web UI, OpenCode, or anything else, with one auth story and one deployment.

**"Is ChatGPT / Codex / Claude Code an LLM or a harness?"** Both, in common speech — but technically: the *model* is GPT-4o/Claude Sonnet, the *harness* is the ChatGPT/Codex/Claude Code application around it. When we say "the LLM decided to call `read_file`", we mean the model chose it; when we say "the harness dispatched `read_file` to mymcp", we mean the application did the plumbing. **Codex** is the clearest example: it is OpenAI's harness (terminal app + cloud agent) that *uses* GPT models and, as an MCP client, can call MCP servers like mymcp for tools.

---

## 8. Further reading

- `README.md` — install, run, deploy, and token management for mymcp/myagent.
- `DESIGN.md` — this server's non-goals, transport details, and package layout.
- [MCP specification](https://modelcontextprotocol.io/) — the upstream standard (generic MCP).
- [truemtls](https://github.com/brywil/truemtls) — the mTLS front for LAN/remote use with this server.
