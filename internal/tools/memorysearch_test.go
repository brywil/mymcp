package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func memWorkspace(t *testing.T) *Registry {
	t.Helper()
	base := t.TempDir()
	// Memory is namespaced per principal: <MemoryDir>/<principal>/. With no principal in the
	// context that is "shared", so the fixture has to write there -- putting MEMORY.md at the
	// base gives "No memory files found" and every assertion fails for the wrong reason.
	dir := filepath.Join(base, "shared")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := "## Tools\nThe assistant has date_now, memory_read and web_search_free.\n" +
		"## Quantization\nKeep routers at fp32 to minimise routing error.\n"
	if err := os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	r := NewRegistry()
	RegisterAll(r, Config{Workspace: base, MemoryDir: base, AllowExec: false})
	return r
}

func search(t *testing.T, r *Registry, q string) string {
	t.Helper()
	out, isErr := r.CallTool(context.Background(), "memory_search", map[string]interface{}{"query": q})
	if isErr {
		t.Fatalf("memory_search(%q) errored: %s", q, out)
	}
	return out
}

// The exact-phrase path must keep working: it is right when it hits.
func TestMemorySearchExactPhrase(t *testing.T) {
	r := memWorkspace(t)
	if out := search(t, r, "routing error"); !strings.Contains(out, "Memory Search Results") {
		t.Errorf("exact phrase no longer matches:\n%s", out)
	}
}

// The reported bug: a model searches with words that are all present but not adjacent,
// and gets nothing. Before the fallback this returned "No matches found".
func TestMemorySearchAllTermsOutOfOrder(t *testing.T) {
	r := memWorkspace(t)
	out := search(t, r, "web_search_free date_now")
	if !strings.Contains(out, "Memory Search Results") {
		t.Errorf("all-terms fallback did not fire; both words are on one line:\n%s", out)
	}
}

// A sentence still should NOT match -- precision matters -- but the failure must teach
// the caller what to do instead, which is what turns a dead end into a retry.
func TestMemorySearchSentenceFailsHelpfully(t *testing.T) {
	r := memWorkspace(t)
	out := search(t, r, "what tools do I have")
	if strings.Contains(out, "Memory Search Results") {
		t.Fatalf("a whole sentence should not match:\n%s", out)
	}
	for _, want := range []string{"LITERAL", "tools", "memory_read"} {
		if !strings.Contains(out, want) {
			t.Errorf("unhelpful failure, missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "appear nowhere") {
		t.Errorf("should name the terms that are absent:\n%s", out)
	}
}
