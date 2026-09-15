package tools

import (
	"context"
	"strings"
	"testing"
	"time"
)

// How an agent actually navigates: orient, then drill in. Includes the site whose renderer wedges,
// to prove the tool degrades instead of hanging the turn.
func TestNavigationSession(t *testing.T) {
	if testing.Short() {
		t.Skip("live")
	}
	r := NewRegistry()
	RegisterAll(r, Config{Workspace: "/home/bryan", CacheDir: "/home/bryan/.goclaw"})
	call := func(args map[string]interface{}) (string, time.Duration, bool) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		t0 := time.Now()
		out, isErr := r.CallTool(ctx, "web_inspect", args)
		return out, time.Since(t0), isErr
	}

	for _, tc := range []struct{ name, url string }{
		{"renders fine", "https://github.com/ggml-org/llama.cpp"},
		{"renderer wedges", "https://huggingface.co/Myric/Ornith-1.5-35B-A3B-APEX-GGUF"},
	} {
		out, d, isErr := call(map[string]interface{}{"url": tc.url, "mode": "outline", "max_chars": 1400})
		t.Logf("=== %s (%s) %.1fs err=%v %d chars ===\n%s", tc.name, tc.url, d.Seconds(), isErr, len(out), out)
		if isErr {
			t.Errorf("%s: returned an error instead of degrading: %s", tc.name, out)
		}
		if d > 55*time.Second {
			t.Errorf("%s: took %.0fs -- should bound and fall back", tc.name, d.Seconds())
		}
		if strings.TrimSpace(out) == "" {
			t.Errorf("%s: empty reply", tc.name)
		}
	}
}
