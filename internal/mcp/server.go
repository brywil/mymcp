package mcp

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
)

// Options configures a Server. Authentication is enforced at the TLS layer
// (mandatory mTLS + trust store); Authz enforces per-CN capability policy.
type Options struct {
	Tools      ToolProvider
	Authz      Authorizer
	ServerName string
	Version    string
	Logger     *log.Logger
}

// Server is an HTTP handler implementing MCP over Streamable HTTP.
type Server struct{ o Options }

// NewServer builds a Server, applying defaults.
func NewServer(o Options) *Server {
	if o.ServerName == "" {
		o.ServerName = "mymcp"
	}
	if o.Logger == nil {
		o.Logger = log.Default()
	}
	return &Server{o: o}
}

// ServeHTTP routes CORS preflight, health, and the /mcp endpoint.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setCORS(w, r)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	switch r.URL.Path {
	case "/healthz":
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"status":"ok"}`)
	case "/mcp", "/":
		s.handleMCP(w, r)
	default:
		http.NotFound(w, r)
	}
}

func setCORS(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if origin == "" {
		origin = "*"
	}
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", origin)
	h.Set("Vary", "Origin")
	h.Set("Access-Control-Allow-Methods", "POST, GET, DELETE, OPTIONS")
	h.Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Mcp-Session-Id, Mcp-Protocol-Version")
	h.Set("Access-Control-Expose-Headers", "Mcp-Session-Id")
	h.Set("Access-Control-Max-Age", "86400")
}

// clientCN returns the peer certificate's Subject CommonName (the principal).
func clientCN(r *http.Request) string {
	if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
		return r.TLS.PeerCertificates[0].Subject.CommonName
	}
	return ""
}

func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodDelete:
		w.WriteHeader(http.StatusOK)
		return
	case http.MethodGet:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	case http.MethodPost:
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	cn := clientCN(r)
	if s.o.Authz != nil && !s.o.Authz.Known(cn) {
		s.o.Logger.Printf("DENIED unknown principal cn=%q — register with: mymcp allow %q ro", cn, cn)
		http.Error(w, `{"error":"unknown principal"}`, http.StatusForbidden)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		writeSingle(w, errResponse(nil, CodeParseError, "read error"))
		return
	}
	trimmed := bytes.TrimSpace(body)
	batch := len(trimmed) > 0 && trimmed[0] == '['

	var reqs []Request
	if batch {
		if err := json.Unmarshal(trimmed, &reqs); err != nil {
			writeSingle(w, errResponse(nil, CodeParseError, "invalid JSON batch"))
			return
		}
	} else {
		var one Request
		if err := json.Unmarshal(trimmed, &one); err != nil {
			writeSingle(w, errResponse(nil, CodeParseError, "invalid JSON"))
			return
		}
		reqs = []Request{one}
	}

	var resps []Response
	sessionSet := false
	for _, req := range reqs {
		resp, isInit := s.process(r.Context(), req, cn)
		if isInit && !sessionSet {
			w.Header().Set("Mcp-Session-Id", newSessionID())
			sessionSet = true
		}
		if resp != nil {
			resps = append(resps, *resp)
		}
	}

	if len(resps) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if batch {
		_ = json.NewEncoder(w).Encode(resps)
	} else {
		_ = json.NewEncoder(w).Encode(resps[0])
	}
}

func writeSingle(w http.ResponseWriter, resp *Response) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// allowed reports whether principal cn may use the named tool.
func (s *Server) allowed(cn, tool string) bool {
	if s.o.Authz == nil {
		return true
	}
	return s.o.Authz.Allow(cn, tool, s.o.Tools.ReadOnly(tool))
}

func (s *Server) process(ctx context.Context, req Request, cn string) (resp *Response, isInit bool) {
	switch req.Method {
	case "initialize":
		pv := DefaultProtocolVersion
		if len(req.Params) > 0 {
			var p initializeParams
			if json.Unmarshal(req.Params, &p) == nil && p.ProtocolVersion != "" {
				pv = p.ProtocolVersion
			}
		}
		return okResponse(req.ID, InitializeResult{
			ProtocolVersion: pv,
			Capabilities:    Capabilities{Tools: &ToolsCapability{ListChanged: false}},
			ServerInfo:      ServerInfo{Name: s.o.ServerName, Version: s.o.Version},
		}), true
	case "ping":
		return okResponse(req.ID, struct{}{}), false
	case "tools/list":
		all := s.o.Tools.ListTools()
		visible := make([]ToolDef, 0, len(all))
		for _, t := range all {
			if s.allowed(cn, t.Name) {
				visible = append(visible, t) // only advertise what this principal may call
			}
		}
		return okResponse(req.ID, ToolsListResult{Tools: visible}), false
	case "tools/call":
		var p callToolParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return errResponse(req.ID, CodeInvalidParams, "invalid params"), false
		}
		if !s.allowed(cn, p.Name) {
			return okResponse(req.ID, CallToolResult{
				Content: []ToolContent{{Type: "text", Text: "not authorized: principal " + cn + " may not call " + p.Name}},
				IsError: true,
			}), false
		}
		text, isErr := s.o.Tools.CallTool(ctx, p.Name, p.Arguments)
		return okResponse(req.ID, CallToolResult{
			Content: []ToolContent{{Type: "text", Text: text}},
			IsError: isErr,
		}), false
	default:
		if req.IsNotification() {
			return nil, false
		}
		return errResponse(req.ID, CodeMethodNotFound, "method not found: "+req.Method), false
	}
}

func newSessionID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
