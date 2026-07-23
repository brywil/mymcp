// Command mymcp is a local MCP tool server: it exposes host tools (file I/O,
// run_command, git, http, system) to any MCP client over plain HTTP on
// loopback, gated by named bearer tokens. It is local-only by default — for LAN
// or remote access, front it with truemtls (https://github.com/brywil/truemtls),
// which adds mandatory mTLS in front of any plain-HTTP backend.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/brywil/mymcp/internal/mcp"
	"github.com/brywil/mymcp/internal/tokens"
	"github.com/brywil/mymcp/internal/tools"
)

const version = "0.2.0"

func main() {
	log.SetFlags(log.LstdFlags)
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = runServe(os.Args[2:])
	case "token":
		err = runToken(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `mymcp — local MCP tool server (plain HTTP, loopback-only, bearer tokens)

usage:
  mymcp serve [flags]           run the tool server
  mymcp token                   print the "default" bearer token (create if absent)
  mymcp token add <name>        create and print a new named token
  mymcp token list              list token names
  mymcp token show <name>       print a token's value
  mymcp token revoke <name>     delete a token

serve flags:
  --addr ADDR         listen address (default 127.0.0.1:9443; loopback only)
  --workspace DIR     root the filesystem tools are confined to (default .)
  --allow-exec        enable run_command + git tools (default true)
  --no-auth           disable bearer auth entirely (open; loopback only)
  --allow-remote      permit a non-loopback bind (prefer fronting with truemtls)

Tokens live one file per name in ~/.config/mymcp/tokens/; revoke by deleting the
file or "mymcp token revoke <name>". Each request is logged with the matching
token name. For LAN/remote, keep mymcp on loopback and front it with mTLS:
  truemtls serve --backend http://127.0.0.1:9443 --listen 0.0.0.0:8443
`)
}

func defaultConfigDir() string {
	if c, err := os.UserConfigDir(); err == nil {
		return filepath.Join(c, "mymcp")
	}
	return filepath.Join(os.Getenv("HOME"), ".config", "mymcp")
}

func defaultTokensDir() string { return filepath.Join(defaultConfigDir(), "tokens") }

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:9443", "listen address (loopback only unless --allow-remote)")
	workspace := fs.String("workspace", ".", "root directory the filesystem tools are confined to")
	allowExec := fs.Bool("allow-exec", true, "enable run_command and git tools")
	noAuth := fs.Bool("no-auth", false, "disable bearer auth (open server; loopback only)")
	allowRemote := fs.Bool("allow-remote", false, "permit binding a non-loopback address")
	_ = fs.Parse(args)

	ws, err := filepath.Abs(*workspace)
	if err != nil {
		return err
	}
	host, _, err := net.SplitHostPort(*addr)
	if err != nil {
		return fmt.Errorf("invalid --addr %q: %w", *addr, err)
	}
	if !*allowRemote && !isLoopback(host) {
		return fmt.Errorf("refusing to bind non-loopback address %q — mymcp is local-only.\n"+
			"For LAN/remote access, keep mymcp on loopback and front it with truemtls (mTLS):\n"+
			"  truemtls serve --backend http://127.0.0.1:9443 --listen 0.0.0.0:8443\n"+
			"Or pass --allow-remote to override.", *addr)
	}

	var authenticate func(string) (string, bool)
	authLabel := "disabled (--no-auth)"
	if !*noAuth {
		store, err := tokens.Load(defaultTokensDir())
		if err != nil {
			return err
		}
		if created, _ := store.EnsureDefault(); created {
			log.Printf("created a default bearer token in %s", store.Dir())
		}
		authenticate = store.Authenticate
		authLabel = fmt.Sprintf("bearer (%d token(s) in %s)", store.Count(), store.Dir())
	}

	reg := tools.NewRegistry()
	tools.RegisterAll(reg, tools.Config{Workspace: ws, AllowExec: *allowExec, ExecTimeout: 120 * time.Second})
	srv := mcp.NewServer(mcp.Options{Tools: reg, Authenticate: authenticate, ServerName: "mymcp", Version: version})

	if *allowRemote && !isLoopback(host) && *noAuth {
		log.Printf("mymcp %s: WARNING — unauthenticated server on http://%s; anyone who can reach it gets RCE", version, *addr)
	}
	log.Printf("mymcp %s: MCP server on http://%s  (workspace=%s, tools=%d, exec=%v, auth=%s)", version, *addr, ws, reg.Count(), *allowExec, authLabel)
	if authenticate != nil {
		log.Printf("retrieve a token for your MCP client with: mymcp token")
	}
	return (&http.Server{Addr: *addr, Handler: srv}).ListenAndServe()
}

func runToken(args []string) error {
	store, err := tokens.Load(defaultTokensDir())
	if err != nil {
		return err
	}
	if len(args) == 0 {
		t, err := store.GetOrCreate("default")
		if err != nil {
			return err
		}
		fmt.Println(t)
		return nil
	}
	switch args[0] {
	case "list":
		names, err := store.List()
		if err != nil {
			return err
		}
		for _, n := range names {
			fmt.Println(n)
		}
		return nil
	case "add":
		if len(args) != 2 {
			return fmt.Errorf("usage: mymcp token add <name>")
		}
		t, err := store.Add(args[1])
		if err != nil {
			return err
		}
		fmt.Println(t)
		return nil
	case "show":
		if len(args) != 2 {
			return fmt.Errorf("usage: mymcp token show <name>")
		}
		t, err := store.Get(args[1])
		if err != nil {
			return err
		}
		fmt.Println(t)
		return nil
	case "revoke":
		if len(args) != 2 {
			return fmt.Errorf("usage: mymcp token revoke <name>")
		}
		if err := store.Revoke(args[1]); err != nil {
			return err
		}
		fmt.Printf("revoked %q\n", args[1])
		return nil
	default:
		return fmt.Errorf("unknown: token %s (use list|add|show|revoke)", args[0])
	}
}

func isLoopback(host string) bool {
	if host == "localhost" || host == "" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
