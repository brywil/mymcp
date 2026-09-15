package tools

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Live test: needs the local chromium. Skipped with -short.
func TestWebInspectLive(t *testing.T) {
	if testing.Short() {
		t.Skip("live browser test")
	}
	r := NewRegistry()
	RegisterAll(r, Config{Workspace: "/home/bryan", CacheDir: "/home/bryan/.goclaw"})
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// NOTE: CallTool's bool is isError, NOT "found". ok==true means the call FAILED.
	out, isErr := r.CallTool(ctx, "web_inspect", map[string]interface{}{
		"url": "https://example.com", "mode": "outline"})
	if isErr {
		t.Fatalf("web_inspect failed: %s", out)
	}
	t.Logf("OUTLINE:\n%s", out)
	if !strings.Contains(strings.ToLower(out), "example") {
		t.Errorf("outline does not mention the page: %q", out)
	}

	out2, _ := r.CallTool(ctx, "web_inspect", map[string]interface{}{
		"url": "https://example.com", "mode": "text"})
	t.Logf("TEXT:\n%s", out2)

	out3, _ := r.CallTool(ctx, "web_inspect", map[string]interface{}{
		"url": "https://example.com", "mode": "query", "selector": "h1"})
	t.Logf("QUERY h1:\n%s", out3)
}
