// Command mymcp is a self-contained MCP tool server with mandatory mTLS and
// per-principal (CN) capability control. See DESIGN.md.
package main

import (
	"crypto/tls"
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
	"github.com/brywil/mymcp/internal/policy"
	"github.com/brywil/mymcp/internal/tools"
	"github.com/brywil/truemtls/pki"
	"github.com/brywil/truemtls/trust"
)

const version = "0.1.2"

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
	case "trust":
		err = runTrust(os.Args[2:])
	case "allow":
		err = runAllow(os.Args[2:])
	case "principal":
		err = runPrincipal(os.Args[2:])
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
	fmt.Fprint(os.Stderr, `mymcp — self-contained MCP tool server (mandatory mTLS, per-CN capabilities)

usage:
  mymcp serve [flags]                       run the tool server
  mymcp trust list                          show authorities, pins, and pending
  mymcp trust approve authority <fp>        trust the CA from a pending cert
  mymcp trust pin <fp>                      pin a pending leaf (ad-hoc, no CA)
  mymcp allow <cn> <tool|ro|all>            grant a capability to a principal
  mymcp principal list                      list principals and grants
  mymcp principal rm <cn>                   remove a principal

Common flags: --config-dir (default ~/.config/mymcp)
`)
}

func defaultConfigDir() string {
	if c, err := os.UserConfigDir(); err == nil {
		return filepath.Join(c, "mymcp")
	}
	return filepath.Join(os.Getenv("HOME"), ".config", "mymcp")
}

// --- serve ---

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", "0.0.0.0:9443", "listen address")
	configDir := fs.String("config-dir", defaultConfigDir(), "config directory")
	trustDir := fs.String("trust-dir", "", "trust root (default <config-dir>/trust)")
	principalsDir := fs.String("principals-dir", "", "principals dir (default <config-dir>/principals)")
	clientCA := fs.String("client-ca", "", "comma-separated extra CA PEM files (always trusted)")
	serverCert := fs.String("server-cert", "", "server cert PEM (default <config-dir>/server.crt)")
	serverKey := fs.String("server-key", "", "server key PEM (default <config-dir>/server.key)")
	workspace := fs.String("workspace", ".", "root directory filesystem tools are confined to")
	allowExec := fs.Bool("allow-exec", true, "enable run_command and git tools")
	_ = fs.Parse(args)

	resolveDirs(*configDir, trustDir, principalsDir, serverCert, serverKey)
	ws, err := filepath.Abs(*workspace)
	if err != nil {
		return err
	}

	store, err := trust.Load(*trustDir, splitCSV(*clientCA), log.Default())
	if err != nil {
		return err
	}
	enf, err := policy.Load(*principalsDir)
	if err != nil {
		return err
	}
	reg := tools.NewRegistry()
	tools.RegisterAll(reg, tools.Config{Workspace: ws, AllowExec: *allowExec, ExecTimeout: 120 * time.Second})

	host, _, _ := net.SplitHostPort(*addr)
	if err := pki.EnsureServerCert(*serverCert, *serverKey, serverHosts(host)); err != nil {
		return fmt.Errorf("server cert: %w", err)
	}
	cert, err := tls.LoadX509KeyPair(*serverCert, *serverKey)
	if err != nil {
		return err
	}

	srv := mcp.NewServer(mcp.Options{Tools: reg, Authz: enf, ServerName: "mymcp", Version: version})
	httpSrv := &http.Server{
		Addr:    *addr,
		Handler: srv,
		TLSConfig: &tls.Config{
			Certificates:          []tls.Certificate{cert},
			ClientAuth:            tls.RequireAnyClientCert, // trust decision is ours, in Verify
			VerifyPeerCertificate: store.Verify,
			MinVersion:            tls.VersionTLS12,
		},
	}
	log.Printf("mymcp %s serving on https://%s  (workspace=%s, tools=%d, exec=%v)", version, *addr, ws, reg.Count(), *allowExec)
	log.Printf("trust dir: %s   principals: %s", *trustDir, *principalsDir)
	return httpSrv.ListenAndServeTLS("", "")
}

func serverHosts(addrHost string) []string {
	hosts := []string{"localhost", "127.0.0.1", "::1"}
	if h, _ := os.Hostname(); h != "" {
		hosts = append(hosts, h)
	}
	if addrHost != "" && addrHost != "0.0.0.0" && addrHost != "::" {
		hosts = append(hosts, addrHost)
	}
	// include all local IPs so LAN clients can verify the server cert
	if ifaces, err := net.InterfaceAddrs(); err == nil {
		for _, a := range ifaces {
			if ipnet, ok := a.(*net.IPNet); ok {
				hosts = append(hosts, ipnet.IP.String())
			}
		}
	}
	return hosts
}

// --- trust ---

func runTrust(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: mymcp trust <list|approve|pin> ...")
	}
	store, err := trust.Load(trustDirFromEnv(), nil, log.Default())
	if err != nil {
		return err
	}
	switch args[0] {
	case "list":
		return trustList(store)
	case "approve":
		if len(args) != 3 || args[1] != "authority" {
			return fmt.Errorf("usage: mymcp trust approve authority <fp>")
		}
		e, err := store.ApproveAuthority(args[2])
		if err != nil {
			return err
		}
		fmt.Printf("trusted authority from pending cert cn=%q (%s)\n", e.CN, e.Fingerprint[:16])
		return nil
	case "pin":
		if len(args) != 2 {
			return fmt.Errorf("usage: mymcp trust pin <fp>")
		}
		e, err := store.Pin(args[1])
		if err != nil {
			return err
		}
		fmt.Printf("pinned leaf cn=%q (%s)\n", e.CN, e.Fingerprint[:16])
		return nil
	default:
		return fmt.Errorf("unknown: trust %s", args[0])
	}
}

func trustList(store *trust.Store) error {
	auth, _ := store.Authorities()
	pins, _ := store.Pins()
	pend, _ := store.Pending()
	section := func(title string, es []trust.Entry) {
		fmt.Printf("%s (%d):\n", title, len(es))
		for _, e := range es {
			fmt.Printf("  %-16s cn=%q issuer=%q ca=%v\n", e.Fingerprint[:16], e.CN, e.Issuer, e.IsCA)
		}
	}
	section("authorities", auth)
	section("pinned", pins)
	section("pending", pend)
	return nil
}

// --- allow / principal ---

func runAllow(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: mymcp allow <cn> <tool|ro|all>")
	}
	enf, err := policy.Load(principalsDirFromEnv())
	if err != nil {
		return err
	}
	grants, err := enf.Grant(args[0], args[1])
	if err != nil {
		return err
	}
	fmt.Printf("principal %q now allows: %s\n", args[0], strings.Join(grants, ", "))
	return nil
}

func runPrincipal(args []string) error {
	enf, err := policy.Load(principalsDirFromEnv())
	if err != nil {
		return err
	}
	if len(args) == 0 {
		args = []string{"list"}
	}
	switch args[0] {
	case "list":
		for _, p := range enf.List() {
			fmt.Printf("%-24q allow=%s\n", p.CN, strings.Join(p.Allow, ", "))
		}
		return nil
	case "rm":
		if len(args) != 2 {
			return fmt.Errorf("usage: mymcp principal rm <cn>")
		}
		if err := enf.Remove(args[1]); err != nil {
			return err
		}
		fmt.Printf("removed principal %q\n", args[1])
		return nil
	default:
		return fmt.Errorf("unknown: principal %s", args[0])
	}
}

// --- shared dir resolution ---

func resolveDirs(configDir string, trustDir, principalsDir, serverCert, serverKey *string) {
	if *trustDir == "" {
		*trustDir = filepath.Join(configDir, "trust")
	}
	if *principalsDir == "" {
		*principalsDir = filepath.Join(configDir, "principals")
	}
	if *serverCert == "" {
		*serverCert = filepath.Join(configDir, "server.crt")
	}
	if *serverKey == "" {
		*serverKey = filepath.Join(configDir, "server.key")
	}
}

func trustDirFromEnv() string      { return filepath.Join(configDirFromEnv(), "trust") }
func principalsDirFromEnv() string { return filepath.Join(configDirFromEnv(), "principals") }

func configDirFromEnv() string {
	if d := os.Getenv("MYMCP_CONFIG_DIR"); d != "" {
		return d
	}
	return defaultConfigDir()
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
