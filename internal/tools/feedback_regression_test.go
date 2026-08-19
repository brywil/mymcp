package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// These are regressions for silent-misbehaviour bugs that were found by
// executing the tools rather than reading them: each one used to return an
// answer that looked plausible but was wrong, or a message that sent the
// caller after something that was never there. A plain "no output" / empty
// string is indistinguishable from a correct empty result, which is exactly
// the failure these pin down.

// --- sed -----------------------------------------------------------------

// case_insensitive must match regardless of the line's case AND preserve the
// case of the parts of the line the match never touched. The old code lowered
// the whole line first, so "Hello WORLD" became "hello world" — rewriting text
// the substitution never asked for.
func TestSedCaseInsensitivePreservesCase(t *testing.T) {
	root := t.TempDir()
	s := &sysTools{root: root}
	p := filepath.Join(root, "in.txt")
	if err := os.WriteFile(p, []byte("Hello WORLD foo\n"), 0644); err != nil {
		t.Fatal(err)
	}
	out, err := s.sed(context.Background(), map[string]interface{}{
		"path": "in.txt", "pattern": "world", "replacement": "EARTH",
		"case_insensitive": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "1 occurrence") {
		t.Fatalf("expected a substitution, got: %s", out)
	}
	data, _ := os.ReadFile(p)
	if got := strings.TrimSpace(string(data)); got != "Hello EARTH foo" {
		t.Errorf("line case was mangled: got %q, want %q", got, "Hello EARTH foo")
	}
}

// A case-sensitive pattern must not match a different case — the flag has to
// be doing the work, not the other way around.
func TestSedCaseSensitiveLeavesOtherCase(t *testing.T) {
	root := t.TempDir()
	s := &sysTools{root: root}
	p := filepath.Join(root, "in.txt")
	if err := os.WriteFile(p, []byte("Hello WORLD foo\n"), 0644); err != nil {
		t.Fatal(err)
	}
	out, err := s.sed(context.Background(), map[string]interface{}{
		"path": "in.txt", "pattern": "world", "replacement": "EARTH",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "0 occurrence") {
		t.Errorf("case-sensitive pattern should not match, got: %s", out)
	}
}

// --- awk -----------------------------------------------------------------

// Comma-separated print args must emit every field. The old parser dropped
// everything after the first field, so "{ print $1, $2 }" printed only $1 —
// silently losing data the caller asked for.
func TestAwkPrintCommaFields(t *testing.T) {
	root := t.TempDir()
	s := &sysTools{root: root}
	p := filepath.Join(root, "in.txt")
	if err := os.WriteFile(p, []byte("alpha beta gamma\n"), 0644); err != nil {
		t.Fatal(err)
	}
	out, err := s.awk(context.Background(), map[string]interface{}{
		"path": "in.txt", "program": "{ print $1, $2 }",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out); got != "alpha beta" {
		t.Errorf("got %q, want %q", got, "alpha beta")
	}
}

// Pattern + body: only lines whose $1 is "foo" contribute, and only $2.
func TestAwkPatternAndBody(t *testing.T) {
	root := t.TempDir()
	s := &sysTools{root: root}
	p := filepath.Join(root, "in.txt")
	if err := os.WriteFile(p, []byte("foo bar\nbaz qux\n"), 0644); err != nil {
		t.Fatal(err)
	}
	out, err := s.awk(context.Background(), map[string]interface{}{
		"path": "in.txt", "program": `$1 == "foo" { print $2 }`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out); got != "bar" {
		t.Errorf("got %q, want %q", got, "bar")
	}
}

// Constructs we do not implement must fail loudly. Returning "(no output)"
// here would read as "the file has no such lines" — a wrong answer dressed as
// a result.
func TestAwkUnsupportedErrors(t *testing.T) {
	root := t.TempDir()
	s := &sysTools{root: root}
	p := filepath.Join(root, "in.txt")
	if err := os.WriteFile(p, []byte("alpha beta\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, prog := range []string{
		`{ printf "%s\n", $1 }`,
		`BEGIN { print "x" }`,
		`$1 > 5 { print $1 }`,
	} {
		if _, err := s.awk(context.Background(), map[string]interface{}{"path": "in.txt", "program": prog}); err == nil {
			t.Errorf("program %q: expected an error, got none", prog)
		} else {
			t.Logf("program %q => %v", prog, err)
		}
	}
}

// --- grep ----------------------------------------------------------------

// case_insensitive must find the line regardless of the case it appears in.
// The old code lowercased the file text, so the pattern "FOO" failed to match
// the line "FOO baz" — the flag actively hid the lines it was meant to find.
func TestGrepCaseInsensitiveBothCases(t *testing.T) {
	root := t.TempDir()
	s := &sysTools{root: root}
	p := filepath.Join(root, "in.txt")
	if err := os.WriteFile(p, []byte("FOO baz\nfoo bar\n"), 0644); err != nil {
		t.Fatal(err)
	}
	out, err := s.grep(context.Background(), map[string]interface{}{
		"pattern": "FOO", "path": "in.txt", "case_insensitive": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "FOO baz") || !strings.Contains(out, "foo bar") {
		t.Errorf("case-insensitive grep missed a case:\n%s", out)
	}
	if !strings.Contains(out, "2 matches") {
		t.Errorf("expected 2 matches, got:\n%s", out)
	}
}

// A directory walk that skips non-default extensions must say so, and
// all_files=true must include them. Silently searching a subset of the tree
// is a wrong answer the caller cannot detect.
func TestGrepSkippedExtensionNote(t *testing.T) {
	root := t.TempDir()
	s := &sysTools{root: root}
	if err := os.WriteFile(filepath.Join(root, "a.rs"), []byte("needle here\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.go"), []byte("needle here\n"), 0644); err != nil {
		t.Fatal(err)
	}

	out, err := s.grep(context.Background(), map[string]interface{}{
		"pattern": "needle", "path": ".",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "skipped") || !strings.Contains(out, ".rs") {
		t.Errorf("missing a note about the skipped .rs file:\n%s", out)
	}
	if strings.Contains(out, "a.rs") {
		t.Errorf(".rs should be skipped by default:\n%s", out)
	}

	out2, err := s.grep(context.Background(), map[string]interface{}{
		"pattern": "needle", "path": ".", "all_files": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out2, "a.rs") || !strings.Contains(out2, "b.go") {
		t.Errorf("all_files=true should include both files:\n%s", out2)
	}
}

// A directory search cut short by max_results must report a lower bound, not a
// precise count (which would be a lie the caller can't detect).
func TestGrepDirectoryTruncationLowerBound(t *testing.T) {
	root := t.TempDir()
	s := &sysTools{root: root}
	var b strings.Builder
	for i := 0; i < 10; i++ {
		b.WriteString("match line\n")
	}
	if err := os.WriteFile(filepath.Join(root, "big.txt"), []byte(b.String()), 0644); err != nil {
		t.Fatal(err)
	}
	out, err := s.grep(context.Background(), map[string]interface{}{
		"pattern": "match", "path": ".", "max_results": 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "at least") {
		t.Errorf("truncated directory search must say 'at least N', got:\n%s", out)
	}
	if !strings.Contains(out, "showing the first 5") {
		t.Errorf("must report how many were shown, got:\n%s", out)
	}
}

// --- parse_css -----------------------------------------------------------

// The combinators the schema advertises must actually work, and selectors we
// do not implement must error rather than silently matching nothing.
func TestParseCSSCombinators(t *testing.T) {
	ht := &httpTools{}
	html := `<html><body>` +
		`<div><p>direct</p></div>` +
		`<div><span><p>nested</p></span></div>` +
		`<h1>h</h1><p>after</p>` +
		`<h1>h</h1><i>x</i><p>sib</p>` +
		`<p class='note'>hello world</p>` +
		`</body></html>`

	count := func(t *testing.T, sel string) int {
		t.Helper()
		out, err := ht.parseCSS(context.Background(), map[string]interface{}{
			"input": html, "selector": sel,
		})
		if err != nil {
			t.Fatalf("selector %q: %v", sel, err)
		}
		n := 0
		if strings.HasPrefix(out, "Found ") {
			// "Found N matching elements (selector: ...)"
			s := strings.TrimPrefix(out, "Found ")
			s = strings.TrimSpace(s)
			s = s[:strings.Index(s, " ")]
			n, err = strconv.Atoi(s)
			if err != nil {
				t.Fatalf("could not parse count from %q: %v", out, err)
			}
		}
		return n
	}

	if n := count(t, "div > p"); n != 1 {
		t.Errorf("div > p: want 1, got %d", n)
	}
	if n := count(t, "div p"); n != 2 {
		t.Errorf("div p: want 2, got %d", n)
	}
	if n := count(t, "h1 + p"); n != 1 {
		t.Errorf("h1 + p: want 1, got %d", n)
	}
	if n := count(t, "h1 ~ p"); n != 3 {
		t.Errorf("h1 ~ p: want 3, got %d", n)
	}
	if n := count(t, "p:contains(hello)"); n != 1 {
		t.Errorf("p:contains(hello): want 1, got %d", n)
	}

	for _, bad := range []string{"div::before", "a, b", "div:nth-child(2)"} {
		if _, err := ht.parseCSS(context.Background(), map[string]interface{}{"input": html, "selector": bad}); err == nil {
			t.Errorf("selector %q: expected an error, got none", bad)
		}
	}
}

// --- web_search ----------------------------------------------------------

// When the live search is unavailable, a previously cached result must be
// served and clearly marked as possibly stale — not a bare failure, and not a
// silent pass-off of old data as fresh.
func TestWebSearchCacheFallback(t *testing.T) {
	root := t.TempDir()
	t.Setenv("OLLAMA_API_KEY", "")
	ws := &webSearchTools{root: root}

	cached := &ollamaWebSearchResponse{Results: []ollamaWebSearchResult{
		{Title: "Cached Title", URL: "https://example.com/cached", Content: "old content"},
	}}
	if err := ws.ensureCacheDir(); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(cached)
	if err := os.WriteFile(ws.cachePath("test query"), data, 0644); err != nil {
		t.Fatal(err)
	}

	out, err := ws.webSearch(context.Background(), map[string]interface{}{
		"query": "test query", "mode": "small",
	})
	if err != nil {
		t.Fatalf("expected the cache fallback, got an error: %v", err)
	}
	if !strings.Contains(out, "Cached Title") {
		t.Errorf("cached result not served:\n%s", out)
	}
	if !strings.Contains(out, "cached") || !strings.Contains(out, "stale") {
		t.Errorf("fallback must say the data is cached/stale:\n%s", out)
	}
}

// No cache and no key: an honest error, not an empty string that reads as
// "no results".
func TestWebSearchNoCacheNoKey(t *testing.T) {
	root := t.TempDir()
	t.Setenv("OLLAMA_API_KEY", "")
	ws := &webSearchTools{root: root}
	if _, err := ws.webSearch(context.Background(), map[string]interface{}{
		"query": "brand new query", "mode": "small",
	}); err == nil {
		t.Error("expected an error with no key and no cache")
	}
}

// --- fs ------------------------------------------------------------------

// An empty file and an empty directory must each say so. An empty string is
// indistinguishable from a tool that did nothing at all.
func TestReadFileEmpty(t *testing.T) {
	root := t.TempDir()
	f := &fsTools{root: root}
	if err := os.WriteFile(filepath.Join(root, "empty.txt"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	out, err := f.readFile(context.Background(), map[string]interface{}{"path": "empty.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "empty file") {
		t.Errorf("expected an explicit empty-file note, got %q", out)
	}
}

func TestListDirectoryEmpty(t *testing.T) {
	root := t.TempDir()
	f := &fsTools{root: root}
	sub := filepath.Join(root, "emptydir")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	out, err := f.listDirectory(context.Background(), map[string]interface{}{"path": "emptydir"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "empty directory") {
		t.Errorf("expected an explicit empty-directory note, got %q", out)
	}
}
