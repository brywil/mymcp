package tools

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestWebInspectHostilePage(t *testing.T) {
	if testing.Short() {
		t.Skip("live browser test")
	}
	r := NewRegistry()
	RegisterAll(r, Config{Workspace: "/home/bryan", CacheDir: "/home/bryan/.goclaw"})
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	out, isErr := r.CallTool(ctx, "web_inspect", map[string]interface{}{
		"url": "file:///home/bryan/browser-test/hostile.html", "mode": "text"})
	if isErr {
		t.Fatalf("failed: %s", out)
	}
	t.Logf("TEXT MODE:\n%s", out)
	if !strings.Contains(out, "UNTRUSTED PAGE CONTENT") {
		t.Error("no warning banner on a page carrying forged chat markers")
	}
	if strings.Contains(out, "<|im_start|>") || strings.Contains(out, "<tool_call>") {
		t.Error("a forged marker survived intact")
	}
	o2, _ := r.CallTool(ctx, "web_inspect", map[string]interface{}{
		"url": "file:///home/bryan/browser-test/hostile.html", "mode": "outline"})
	t.Logf("OUTLINE:\n%s", o2)
}
