package tools

import (
	"context"
	"strings"
	"testing"
)

// The point of list_tools is that it reports what the instance ACTUALLY exposes. A version
// that reported the full catalog would be worse than nothing: it would tell a restricted
// assistant about tools that refuse to run, which is exactly the confident-and-wrong answer
// the tool exists to prevent.
func TestListToolsRespectsRestrict(t *testing.T) {
	r := NewRegistry()
	RegisterAll(r, Config{Workspace: t.TempDir(), AllowExec: false})
	if !r.Has("list_tools") {
		t.Fatal("list_tools was not registered")
	}
	full := len(r.ListTools())

	r.Restrict([]string{"list_tools", "date_now", "read_file"})
	out, isErr := r.CallTool(context.Background(), "list_tools", nil)
	if isErr {
		t.Fatalf("list_tools failed: %s", out)
	}
	if !strings.Contains(out, "You have 3 tools") {
		t.Errorf("expected a count of 3 after Restrict, got:\n%s", out)
	}
	for _, want := range []string{"list_tools", "date_now", "read_file"} {
		if !strings.Contains(out, want) {
			t.Errorf("%s missing from the listing:\n%s", want, out)
		}
	}
	// A tool that exists in the registry but is NOT exposed must not be advertised.
	if strings.Contains(out, "web_search_free") {
		t.Errorf("list_tools advertised a restricted-away tool:\n%s", out)
	}
	if full <= 3 {
		t.Fatalf("test is vacuous: the unrestricted catalog was only %d tools", full)
	}
}

func TestListToolsFilter(t *testing.T) {
	r := NewRegistry()
	RegisterAll(r, Config{Workspace: t.TempDir(), AllowExec: false})
	out, isErr := r.CallTool(context.Background(), "list_tools",
		map[string]interface{}{"contains": "memory"})
	if isErr {
		t.Fatalf("failed: %s", out)
	}
	if !strings.Contains(out, "memory_read") || strings.Contains(out, "date_now") {
		t.Errorf("filter did not narrow to memory tools:\n%s", out)
	}
	// A filter matching nothing must say so and say what to do instead, rather than
	// returning an empty list that reads as "you have no tools".
	out, _ = r.CallTool(context.Background(), "list_tools",
		map[string]interface{}{"contains": "zzzznope"})
	if !strings.Contains(out, "No tools match") || !strings.Contains(out, "no arguments") {
		t.Errorf("unhelpful empty-filter response:\n%s", out)
	}
}
