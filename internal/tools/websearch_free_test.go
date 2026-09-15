package tools

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// Drives a real browser against a live Brave query, so it is opt-in: the deploy gate
// runs the suite offline and a network test there would make deploys flaky.
// Run with: MYMCP_LIVE_TESTS=1 go test ./internal/tools/ -run WebSearchFreeLive -v
func TestWebSearchFreeLive(t *testing.T) {
	if os.Getenv("MYMCP_LIVE_TESTS") != "1" {
		t.Skip("set MYMCP_LIVE_TESTS=1 to run (needs goclaw-chromium + network)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	out, err := webSearchFree(ctx, map[string]interface{}{
		"query": "llama.cpp imatrix quantization",
		"count": float64(5),
	})
	if err != nil {
		t.Fatalf("webSearchFree: %v", err)
	}
	if strings.Contains(out, "No results extracted") {
		t.Fatalf("extraction returned nothing -- Brave markup may have changed:\n%s", out)
	}
	if n := strings.Count(out, "https://"); n < 3 {
		t.Fatalf("expected >=3 URLs, got %d:\n%s", n, out)
	}
}

func TestWebSearchFreeRejectsEmptyQuery(t *testing.T) {
	if _, err := webSearchFree(context.Background(), map[string]interface{}{"query": "  "}); err == nil {
		t.Fatal("expected an error for a blank query")
	}
}
