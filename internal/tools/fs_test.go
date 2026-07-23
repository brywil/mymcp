package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveConfinesToWorkspace(t *testing.T) {
	root := t.TempDir()
	f := &fsTools{root: root}

	// Escapes must be rejected.
	for _, p := range []string{"../etc/passwd", "../../x", "/etc/passwd", "sub/../../out"} {
		if _, err := f.resolve(p); err == nil {
			t.Errorf("resolve(%q) should be rejected as escaping the workspace", p)
		}
	}
	// Paths inside the workspace are allowed (relative and absolute-inside).
	if got, err := f.resolve("a/b.txt"); err != nil || got != filepath.Join(root, "a/b.txt") {
		t.Fatalf("resolve(a/b.txt) = %q, %v", got, err)
	}
	if _, err := f.resolve(filepath.Join(root, "inside.txt")); err != nil {
		t.Fatalf("absolute path inside workspace should be allowed: %v", err)
	}
}

func TestWriteReadEditRoundTrip(t *testing.T) {
	root := t.TempDir()
	f := &fsTools{root: root}
	ctx := context.Background()

	if _, err := f.write(ctx, map[string]interface{}{"path": "notes/todo.txt", "content": "hello"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "notes/todo.txt")); string(b) != "hello" {
		t.Fatalf("file content = %q", b)
	}
	out, err := f.read(ctx, map[string]interface{}{"path": "notes/todo.txt"})
	if err != nil || out != "hello" {
		t.Fatalf("read = %q, %v", out, err)
	}
	if _, err := f.edit(ctx, map[string]interface{}{
		"path": "notes/todo.txt", "old_string": "hello", "new_string": "world",
	}); err != nil {
		t.Fatal(err)
	}
	out, _ = f.read(ctx, map[string]interface{}{"path": "notes/todo.txt"})
	if out != "world" {
		t.Fatalf("after edit = %q", out)
	}
}

func TestWriteRejectsEscape(t *testing.T) {
	f := &fsTools{root: t.TempDir()}
	if _, err := f.write(context.Background(), map[string]interface{}{"path": "../escape.txt", "content": "x"}); err == nil {
		t.Fatal("write outside the workspace must be rejected")
	}
}
