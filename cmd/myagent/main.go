// Command myagent is an MCP server that does exactly one thing: let agents talk
// to each other. It is deliberately SEPARATE from mymcp.
//
// Why a second binary rather than more tools in mymcp:
//
//   - Different failure domain. The message store may live on a shared mount; if
//     that stalls, it must not take read_file and run_command down with it.
//   - Different scope. mymcp means "do this on THIS machine". Messaging is
//     explicitly cross-machine; folding them together muddies that boundary.
//   - Different grant. An agent can be given messaging WITHOUT the filesystem or
//     a shell — which is what you want before letting a cloud-hosted model join a
//     conversation.
//   - Independent lifecycle: restart either without dropping the other.
//
// It shares mymcp's token store, so an agent's existing named token identifies it
// to both servers and revoking the token file kills both at once.
//
// Remote agents do NOT need a shared filesystem. They reach this one server over
// mTLS and it keeps the store on its own local disk:
//
//	other host ──mTLS──> truemtls ──plain http──> myagent (loopback) ──> local store
//
// Every participant connects TO the server; the server never dials out. That fits
// a network where connections are only permitted in one direction, and it avoids
// depending on a network filesystem for correctness.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"

	"github.com/brywil/mymcp/internal/mcp"
	"github.com/brywil/mymcp/internal/tokens"
	"github.com/brywil/mymcp/internal/tools"
)

const version = "0.1.0"

func main() {
	log.SetFlags(log.LstdFlags)
	args := os.Args[1:]
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		usage()
		return
	}
	if len(args) > 0 && args[0] == "serve" {
		args = args[1:]
	}
	if err := runServe(args); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `myagent — MCP server for agent-to-agent messaging (plain HTTP, loopback-only, bearer tokens)

usage:
  myagent [serve] [flags]

flags:
  --addr ADDR      listen address (default 127.0.0.1:9444; loopback only)
  --store DIR      message store (default ~/.local/share/myagent)
  --allow-remote   permit a non-loopback bind (prefer fronting with truemtls)

Tools: agent_publish, agent_subscribe, agent_unsubscribe, agent_poll, agent_list.

Identity comes from mymcp's token store (~/.config/mymcp/tokens/) — one NAMED
token per agent, since the token name becomes the agent name other agents
address. Messaging refuses to run unauthenticated: without distinct principals
every caller would share one mailbox.

For other hosts, keep this on loopback and front it with mTLS:
  truemtls serve --backend http://127.0.0.1:9444 --listen 0.0.0.0:8444 \
                 --client-id-header X-Client-CN
Certificates gate WHICH MACHINES may connect; bearer tokens identify WHICH AGENT
is calling. Keep both: truemtls notes that any trusted CA can assert any CN, so a
certificate alone is only as strong as the weakest CA in authorities/.
`)
}

func defaultStoreDir() string {
	if d, err := os.UserHomeDir(); err == nil {
		return filepath.Join(d, ".local", "share", "myagent")
	}
	return "/tmp/myagent"
}

func defaultTokensDir() string {
	if c, err := os.UserConfigDir(); err == nil {
		return filepath.Join(c, "mymcp", "tokens")
	}
	return filepath.Join(os.Getenv("HOME"), ".config", "mymcp", "tokens")
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:9444", "listen address (loopback only unless --allow-remote)")
	store := fs.String("store", "", "message store directory (default ~/.local/share/myagent)")
	allowRemote := fs.Bool("allow-remote", false, "permit binding a non-loopback address")
	_ = fs.Parse(args)

	dir := *store
	if dir == "" {
		dir = defaultStoreDir()
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}

	host, _, err := net.SplitHostPort(*addr)
	if err != nil {
		return fmt.Errorf("invalid --addr %q: %w", *addr, err)
	}
	if !*allowRemote && !isLoopback(host) {
		return fmt.Errorf("refusing to bind non-loopback address %q — myagent is local-only.\n"+
			"For other hosts, keep it on loopback and front it with truemtls (mTLS):\n"+
			"  truemtls serve --backend http://127.0.0.1:9444 --listen 0.0.0.0:8444\n"+
			"Or pass --allow-remote to override.", *addr)
	}

	// Auth is MANDATORY here, unlike mymcp's optional --no-auth. Messaging without
	// distinct principals is not a degraded mode, it is a broken one: every caller
	// would share one mailbox and could publish under one another's name. Better to
	// refuse at startup than to serve tools that reject every call.
	tk, err := tokens.Load(defaultTokensDir())
	if err != nil {
		return err
	}
	names, _ := tk.List()
	if len(names) < 2 {
		log.Printf("NOTE: only %d token(s) in %s. Messaging needs one NAMED token per agent "+
			"(the token name IS the agent name); create them with: mymcp token add <agent>", len(names), tk.Dir())
	}

	reg := tools.NewRegistry()
	if err := tools.RegisterAgentTools(reg, dir); err != nil {
		return fmt.Errorf("message store: %w", err)
	}

	srv := mcp.NewServer(mcp.Options{
		Tools: reg, Authenticate: tk.Authenticate,
		ServerName: "myagent", Version: version,
	})
	log.Printf("myagent %s: MCP server on http://%s  (store=%s, tools=%d, agents=%v)",
		version, *addr, dir, reg.Count(), names)
	return (&http.Server{Addr: *addr, Handler: srv}).ListenAndServe()
}

func isLoopback(host string) bool {
	if host == "localhost" || host == "" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
