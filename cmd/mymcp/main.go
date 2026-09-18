// Command mymcp is a local MCP tool server: it exposes host tools (file I/O,
// run_command, git, http, system) to any MCP client over plain HTTP on
// loopback, gated by named bearer tokens. It is local-only by default — for LAN
// or remote access, front it with truemtls (https://github.com/brywil/truemtls),
// which adds mandatory mTLS in front of any plain-HTTP backend.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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
	case "check-browser":
		err = runCheckBrowser(os.Args[2:])
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
  mymcp check-browser           verify the headless browser end to end, and say what to fix

serve flags:
  --addr ADDR         listen address (default 127.0.0.1:9443; loopback only)
  --workspace DIR     root the filesystem tools are confined to (default .)
  --allow-exec        enable shell-backed tools: run_command, git, gh, tmux (default true)
  --tools LIST        comma-separated whitelist of tools to expose. Accepts the
                      words 'all' and 'ro' (every ReadOnly tool) alongside
                      explicit names, e.g. --tools ro,memory_save
                      Enforced on dispatch, not just on the listing.
  --llama-url URL     deprecated, ignored (ports are discovered from the units by goclaw)
  --llama-model ID    deprecated, ignored (model_info now lives in goclaw)
  --tmux-socket PATH  tmux socket (-S) for tmux tools; empty = default tmux server
  --no-auth           disable bearer auth entirely (open; loopback only)
  --allow-remote      permit a non-loopback bind (prefer fronting with truemtls)

Tokens live one file per name in ~/.config/mymcp/tokens/; revoke by deleting the
file or "mymcp token revoke <name>". Each request is logged with the matching
token name. For LAN/remote, keep mymcp on loopback and front it with mTLS:
  truemtls serve --backend http://127.0.0.1:9443 --listen 0.0.0.0:8443
`)
}

// runCheckBrowser is the install-time and debugging entry point for the browser stack.
// Exit 0 means web_inspect / web_search_free / screenshots all work; non-zero names the
// first broken layer and the fix.
func runCheckBrowser(args []string) error {
	fs := flag.NewFlagSet("check-browser", flag.ExitOnError)
	endpoint := fs.String("cdp-url", "", "DevTools endpoint (default $MYMCP_CDP_URL or http://127.0.0.1:9222)")
	_ = fs.Parse(args)

	ep := *endpoint
	if ep == "" {
		ep = os.Getenv("MYMCP_CDP_URL")
	}
	if ep == "" {
		ep = "http://127.0.0.1:9222"
	}
	fmt.Printf("mymcp check-browser -> %s\n\n", ep)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	report, ok := tools.FormatPreflight(tools.Preflight(ctx, ep))
	fmt.Print(report)
	if !ok {
		return fmt.Errorf("browser preflight failed")
	}
	return nil
}

// logBrowserHealth reports the browser stack once at startup, in the background and never
// fatally: the file, shell and memory tools must come up whether or not a browser exists.
// It runs because the browser tools otherwise fail only at CALL time, with an error that
// says nothing about which layer broke -- and a fresh install has no browser at all.
func logBrowserHealth() {
	ep := os.Getenv("MYMCP_CDP_URL")
	if ep == "" {
		ep = "http://127.0.0.1:9222"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	stages := tools.Preflight(ctx, ep)
	if _, ok := tools.FormatPreflight(stages); ok {
		log.Printf("[BROWSER] %s: healthy (DOM + screenshots)", ep)
		return
	}
	var first tools.PreflightStage
	for _, st := range stages {
		if !st.OK {
			first = st
			break
		}
	}
	log.Printf("[BROWSER] %s: DEGRADED at stage %q (%s). Browser-backed tools "+
		"(web_inspect, web_search_free, screenshots) will fail until this is fixed; "+
		"run `mymcp check-browser` for the remedy.", ep, first.Name, first.Detail)
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
	allowExec := fs.Bool("allow-exec", true, "enable shell-backed tools (run_command, git, gh, tmux)")
	toolList := fs.String("tools", "", "comma-separated whitelist of tools to expose; also accepts the words 'all' and 'ro'. Empty = all")
	// Deprecated: analyze_image and model_info both moved to goclaw (they must
	// follow goclaw's runtime backend switches). Flags still accepted so existing
	// unit files don't break, but ignored.
	_ = fs.String("llama-url", "", "deprecated: unused (analyze_image now lives in goclaw)")
	_ = fs.String("llama-model", "", "deprecated: unused (model_info now lives in goclaw)")
	tmuxSocket := fs.String("tmux-socket", "", "tmux socket path (-S); empty uses the default tmux server")
	memoryDir := fs.String("memory-dir", "", "base dir for per-agent memory (<memory-dir>/<token-name>/); empty uses --workspace")
	cacheDir := fs.String("cache-dir", "", "base dir for generated scratch (web_cache, images); empty uses the OS cache dir (~/.cache/mymcp)")
	noAuth := fs.Bool("no-auth", false, "disable bearer auth (open server; loopback only)")
	allowRemote := fs.Bool("allow-remote", false, "permit binding a non-loopback address")
	_ = fs.Parse(args)

	// Also settable from the environment, because the systemd unit is shared across machines
	// while the bind address is per-machine. Expanding a flag from an EnvironmentFile instead
	// (--allow-remote=${ALLOW_REMOTE}) breaks every install that lacks the var: systemd expands
	// it to "--allow-remote=", which flag.Bool rejects, and the server refuses to start.
	// An unset env var is simply false.
	if v := os.Getenv("MYMCP_ALLOW_REMOTE"); v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes") {
		*allowRemote = true
	}

	ws, err := filepath.Abs(*workspace)
	if err != nil {
		return err
	}
	// Default the scratch/cache dir to the OS cache location so generated files
	// (web_cache, images) never land in a whole-home workspace root.
	cache := *cacheDir
	if cache == "" {
		if ucd, err := os.UserCacheDir(); err == nil {
			cache = filepath.Join(ucd, "mymcp")
		}
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
	tools.RegisterAll(reg, tools.Config{
		Workspace:   ws,
		AllowExec:   *allowExec,
		ExecTimeout: 120 * time.Second,
		TmuxSocket:  *tmuxSocket,
		MemoryDir:   *memoryDir,
		CacheDir:    cache,
	})
	// Restrict BEFORE the server is constructed, so there is no window in which
	// the full catalog is reachable.
	exposed := reg.Count()
	if strings.TrimSpace(*toolList) != "" {
		names, err := tools.ResolveTools(reg, []string{*toolList})
		if err != nil {
			log.Fatalf("mymcp: %v", err)
		}
		if len(names) == 0 {
			log.Fatalf("mymcp: --tools %q selected nothing; refusing to start (use --tools all for the full catalog)", *toolList)
		}
		reg.Restrict(names)
		exposed = len(names)
		log.Printf("mymcp: exposing %d of %d tools: %s", exposed, reg.Count(), strings.Join(names, " "))
	}

	srv := mcp.NewServer(mcp.Options{Tools: reg, Authenticate: authenticate, ServerName: "mymcp", Version: version})

	if *allowRemote && !isLoopback(host) && *noAuth {
		log.Printf("mymcp %s: WARNING — unauthenticated server on http://%s; anyone who can reach it gets RCE", version, *addr)
	}
	log.Printf("mymcp %s: MCP server on http://%s  (workspace=%s, tools=%d, exec=%v, auth=%s)", version, *addr, ws, exposed, *allowExec, authLabel)
	if authenticate != nil {
		log.Printf("retrieve a token for your MCP client with: mymcp token")
	}
	go logBrowserHealth()
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
