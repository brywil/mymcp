package mcp_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/brywil/mymcp/internal/mcp"
)

// fakeTools implements mcp.ToolProvider.
type fakeTools struct{}

func (fakeTools) ListTools() []mcp.ToolDef {
	return []mcp.ToolDef{{Name: "read_file"}, {Name: "run_command"}}
}
func (fakeTools) CallTool(_ context.Context, name string, _ map[string]interface{}) (string, bool) {
	return "called:" + name, false
}
func (fakeTools) ReadOnly(name string) bool { return name == "read_file" }

// fakeAuthz implements mcp.Authorizer.
type fakeAuthz struct {
	known bool
	allow map[string]bool
}

func (f fakeAuthz) Known(string) bool                 { return f.known }
func (f fakeAuthz) Allow(_, tool string, _ bool) bool { return f.allow[tool] }

func request(t *testing.T, srv *mcp.Server, cn, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	if cn != "" {
		req.TLS = &tls.ConnectionState{
			PeerCertificates: []*x509.Certificate{{Subject: pkix.Name{CommonName: cn}}},
		}
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

type rpcResp struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func TestUnknownPrincipalForbidden(t *testing.T) {
	srv := mcp.NewServer(mcp.Options{Tools: fakeTools{}, Authz: fakeAuthz{known: false}})
	rec := request(t, srv, "nobody", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("unknown principal should get 403, got %d", rec.Code)
	}
}

func TestToolsListFilteredByCapability(t *testing.T) {
	srv := mcp.NewServer(mcp.Options{
		Tools: fakeTools{},
		Authz: fakeAuthz{known: true, allow: map[string]bool{"read_file": true}}, // run_command NOT allowed
	})
	rec := request(t, srv, "alice", `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var r rpcResp
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	var res struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	_ = json.Unmarshal(r.Result, &res)
	if len(res.Tools) != 1 || res.Tools[0].Name != "read_file" {
		t.Fatalf("tools/list should be filtered to read_file only, got %+v", res.Tools)
	}
}

func TestToolsCallAuthorization(t *testing.T) {
	srv := mcp.NewServer(mcp.Options{
		Tools: fakeTools{},
		Authz: fakeAuthz{known: true, allow: map[string]bool{"read_file": true}},
	})
	// Allowed tool succeeds.
	rec := request(t, srv, "alice", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{}}}`)
	if !strings.Contains(rec.Body.String(), "called:read_file") {
		t.Fatalf("allowed tool call should run: %s", rec.Body.String())
	}
	// Disallowed tool is refused as a tool error.
	rec = request(t, srv, "alice", `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"run_command","arguments":{}}}`)
	body := rec.Body.String()
	if !strings.Contains(body, "not authorized") || strings.Contains(body, "called:run_command") {
		t.Fatalf("disallowed tool must be refused: %s", body)
	}
}

func TestInitializeReturnsServerInfo(t *testing.T) {
	srv := mcp.NewServer(mcp.Options{Tools: fakeTools{}, Authz: fakeAuthz{known: true, allow: map[string]bool{}}, ServerName: "mymcp", Version: "test"})
	rec := request(t, srv, "alice", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"serverInfo"`) {
		t.Fatalf("initialize failed: %d %s", rec.Code, rec.Body.String())
	}
}
