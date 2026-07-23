package mcp_test

import (
	"context"
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

func do(t *testing.T, srv *mcp.Server, authz, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	if authz != "" {
		req.Header.Set("Authorization", authz)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func TestOpenServerAllowsAndDispatches(t *testing.T) {
	srv := mcp.NewServer(mcp.Options{Tools: fakeTools{}, ServerName: "mymcp", Version: "test"})

	if rec := do(t, srv, "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"serverInfo"`) {
		t.Fatalf("initialize: %d %s", rec.Code, rec.Body.String())
	}
	rec := do(t, srv, "", `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	var r struct {
		Result struct {
			Tools []struct{ Name string } `json:"tools"`
		} `json:"result"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &r)
	if len(r.Result.Tools) != 2 {
		t.Fatalf("tools/list should return all tools, got %+v", r.Result.Tools)
	}
	if rec := do(t, srv, "", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"run_command","arguments":{}}}`); !strings.Contains(rec.Body.String(), "called:run_command") {
		t.Fatalf("tools/call: %s", rec.Body.String())
	}
}

// authFunc accepts one token and maps it to a principal name.
func authFunc(token, principal string) func(string) (string, bool) {
	return func(got string) (string, bool) {
		if got == token {
			return principal, true
		}
		return "", false
	}
}

func TestBearerTokenEnforced(t *testing.T) {
	srv := mcp.NewServer(mcp.Options{Tools: fakeTools{}, Authenticate: authFunc("s3cret", "alice")})
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`

	if rec := do(t, srv, "", body); rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing token should be 401, got %d", rec.Code)
	}
	if rec := do(t, srv, "Bearer wrong", body); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token should be 401, got %d", rec.Code)
	}
	if rec := do(t, srv, "Bearer s3cret", body); rec.Code != http.StatusOK {
		t.Fatalf("correct token should be 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCORSPreflightNeedsNoAuth(t *testing.T) {
	srv := mcp.NewServer(mcp.Options{Tools: fakeTools{}, Authenticate: authFunc("s3cret", "alice")})
	req := httptest.NewRequest(http.MethodOptions, "/mcp", nil)
	req.Header.Set("Origin", "http://localhost:8080")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("OPTIONS preflight should be 204 without auth, got %d", rec.Code)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") == "" {
		t.Fatal("preflight must return CORS headers")
	}
}
