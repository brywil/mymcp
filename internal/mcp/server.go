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
	"strings"
)

// Options configures a Server. If Authenticate is non-nil, every /mcp request
// must carry "Authorization: Bearer <token>" that it accepts; it returns a
// principal name used for logging. Nil Authenticate = open (loopback use).
type Options struct {
	Tools        ToolProvider
	Authenticate func(bearer string) (principal string, ok bool)
	ServerName   string
	Version      string
	Logger       *log.Logger
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
		principal := "-"
		if s.o.Authenticate != nil {
			p, ok := s.o.Authenticate(bearerToken(r))
			if !ok {
				s.o.Logger.Printf("DENIED unauthorized request from %s", r.RemoteAddr)
				w.Header().Set("WWW-Authenticate", `Bearer realm="mymcp"`)
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
			principal = p
		}
		s.handleMCP(w, r, principal)
	default:
		http.NotFound(w, r)
	}
}

func bearerToken(r *http.Request) string {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
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

func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request, principal string) {
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
		resp, isInit := s.process(r.Context(), req, principal)
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

func (s *Server) process(ctx context.Context, req Request, principal string) (resp *Response, isInit bool) {
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
		return okResponse(req.ID, ToolsListResult{Tools: s.o.Tools.ListTools()}), false
	case "tools/call":
		var p callToolParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return errResponse(req.ID, CodeInvalidParams, "invalid params"), false
		}
		s.o.Logger.Printf("call principal=%q tool=%s", principal, p.Name) // audit log
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
