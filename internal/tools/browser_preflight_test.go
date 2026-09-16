package tools

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestFormatPreflightReportsFirstFailureAndFix(t *testing.T) {
	stages := []PreflightStage{
		{Name: "reachable", OK: true, Detail: "Chrome/152", Took: 10 * time.Millisecond},
		{Name: "websocket", OK: false, Detail: "bad status", Fix: "add --remote-allow-origins=*"},
	}
	out, ok := FormatPreflight(stages)
	if ok {
		t.Fatal("a failing stage must make the report not-ok")
	}
	for _, want := range []string{"[ok  ] reachable", "[FAIL] websocket", "add --remote-allow-origins=*", "NOT fully working"} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q:\n%s", want, out)
		}
	}
}

func TestFormatPreflightHealthy(t *testing.T) {
	out, ok := FormatPreflight([]PreflightStage{{Name: "screenshot", OK: true, Detail: "1234 bytes"}})
	if !ok {
		t.Fatal("all-ok stages must report ok")
	}
	if !strings.Contains(out, "healthy") {
		t.Errorf("expected a healthy line:\n%s", out)
	}
}

// A connection-refused endpoint must be diagnosed as "not running", and the advice must not
// claim the browser is missing when one is on PATH -- those have completely different fixes
// and an identical symptom.
func TestPreflightUnreachableNamesTheRightProblem(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stages := Preflight(ctx, "http://127.0.0.1:1") // nothing ever listens here
	if len(stages) != 1 {
		t.Fatalf("expected to stop after the first stage, got %d", len(stages))
	}
	if stages[0].Name != "reachable" || stages[0].OK {
		t.Fatalf("expected reachable to fail, got %+v", stages[0])
	}
	if stages[0].Fix == "" {
		t.Error("an unreachable endpoint must carry a fix")
	}
	if !strings.Contains(stages[0].Fix, "DevTools protocol") {
		t.Errorf("fix should explain what is missing, got:\n%s", stages[0].Fix)
	}
}

func TestDigString(t *testing.T) {
	m := map[string]interface{}{"result": map[string]interface{}{"value": "complete"}}
	if got := digString(m, "result", "value"); got != "complete" {
		t.Errorf("digString = %q, want %q", got, "complete")
	}
	// cdpConn.call already unwraps msg["result"], so an over-deep path must not panic.
	if got := digString(m, "result", "result", "value"); got != "" {
		t.Errorf("over-deep path should yield empty, got %q", got)
	}
	if got := digString(nil, "a"); got != "" {
		t.Errorf("nil map should yield empty, got %q", got)
	}
}
