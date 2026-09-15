package tools

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestHFAllModes(t *testing.T) {
	if testing.Short() {
		t.Skip("live")
	}
	r := NewRegistry()
	RegisterAll(r, Config{Workspace: "/home/bryan", CacheDir: "/home/bryan/.goclaw"})
	url := "https://huggingface.co/Myric/Ornith-1.5-35B-A3B-APEX-GGUF"
	for _, tc := range []struct {
		mode, sel string
	}{
		{"outline", ""}, {"text", ""}, {"links", ""}, {"query", "h2"},
	} {
		args := map[string]interface{}{"url": url, "mode": tc.mode, "max_chars": 900}
		if tc.sel != "" {
			args["selector"] = tc.sel
		}
		ctx, cancel := context.WithTimeout(context.Background(), 70*time.Second)
		t0 := time.Now()
		out, isErr := r.CallTool(ctx, "web_inspect", args)
		cancel()
		jsOff := strings.Contains(out, "JAVASCRIPT DISABLED")
		raw := strings.Contains(out, "RAW HTML")
		t.Logf("--- mode=%s %.0fs err=%v jsOff=%v rawFallback=%v %d chars ---\n%s",
			tc.mode, time.Since(t0).Seconds(), isErr, jsOff, raw, len(out), out)
	}
}
