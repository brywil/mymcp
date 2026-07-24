package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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
	// Paths inside the workspace are allowed and stay within root.
	got, err := f.resolve("a/b.txt")
	if err != nil {
		t.Fatalf("resolve(a/b.txt) error: %v", err)
	}
	rootReal, _ := filepath.EvalSymlinks(root)
	if !isWithin(rootReal, got) {
		t.Fatalf("resolve(a/b.txt) = %q, not within %q", got, rootReal)
	}
	if _, err := f.resolve(filepath.Join(root, "inside.txt")); err != nil {
		t.Fatalf("absolute path inside workspace should be allowed: %v", err)
	}
}

func TestWriteReadEditRoundTrip(t *testing.T) {
	root := t.TempDir()
	f := &fsTools{root: root}
	ctx := context.Background()

	if _, err := f.writeFile(ctx, map[string]interface{}{"path": "notes/todo.txt", "content": "hello"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "notes/todo.txt")); string(b) != "hello" {
		t.Fatalf("file content = %q", b)
	}
	out, err := f.readFile(ctx, map[string]interface{}{"path": "notes/todo.txt"})
	if err != nil || out != "hello" {
		t.Fatalf("read = %q, %v", out, err)
	}
	if _, err := f.editFile(ctx, map[string]interface{}{
		"path": "notes/todo.txt", "old_text": "hello", "new_text": "world",
	}); err != nil {
		t.Fatal(err)
	}
	out, _ = f.readFile(ctx, map[string]interface{}{"path": "notes/todo.txt"})
	if out != "world" {
		t.Fatalf("after edit = %q", out)
	}
}

func TestWriteRejectsEscape(t *testing.T) {
	f := &fsTools{root: t.TempDir()}
	if _, err := f.writeFile(context.Background(), map[string]interface{}{"path": "../escape.txt", "content": "x"}); err == nil {
		t.Fatal("write outside the workspace must be rejected")
	}
}

func TestAppendTouchAndPathInfo(t *testing.T) {
	root := t.TempDir()
	f := &fsTools{root: root}
	ctx := context.Background()

	if _, err := f.touch(ctx, map[string]interface{}{"path": "log.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.appendFile(ctx, map[string]interface{}{"path": "log.txt", "content": "a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.appendFile(ctx, map[string]interface{}{"path": "log.txt", "content": "b"}); err != nil {
		t.Fatal(err)
	}
	out, _ := f.readFile(ctx, map[string]interface{}{"path": "log.txt"})
	if out != "ab" {
		t.Fatalf("append result = %q", out)
	}
	info, err := f.pathInfo(ctx, map[string]interface{}{"path": "log.txt"})
	if err != nil || !strings.Contains(info, "Type: file") {
		t.Fatalf("path_info = %q, %v", info, err)
	}
}

func TestCreateDirMoveCopyRenameDelete(t *testing.T) {
	root := t.TempDir()
	f := &fsTools{root: root}
	ctx := context.Background()

	if _, err := f.createDirectory(ctx, map[string]interface{}{"path": "d1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.writeFile(ctx, map[string]interface{}{"path": "d1/x.txt", "content": "data"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.copyFile(ctx, map[string]interface{}{"source_path": "d1/x.txt", "dest_path": "d1/y.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.renameFile(ctx, map[string]interface{}{"old_path": "d1/y.txt", "new_path": "d1/z.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.moveFile(ctx, map[string]interface{}{"old_path": "d1/z.txt", "new_path": "z.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "z.txt")); err != nil {
		t.Fatalf("moved file missing: %v", err)
	}
	// rename_file must reject cross-directory targets.
	if _, err := f.renameFile(ctx, map[string]interface{}{"old_path": "z.txt", "new_path": "d1/z.txt"}); err == nil {
		t.Fatal("rename_file across directories must be rejected")
	}
	// delete a directory requires recursive.
	if _, err := f.deleteFile(ctx, map[string]interface{}{"path": "d1"}); err == nil {
		t.Fatal("deleting a directory without recursive must be rejected")
	}
	if _, err := f.deleteFile(ctx, map[string]interface{}{"path": "d1", "recursive": true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "d1")); !os.IsNotExist(err) {
		t.Fatalf("directory should be gone, err=%v", err)
	}
}

func TestReadFileRejectsImage(t *testing.T) {
	root := t.TempDir()
	f := &fsTools{root: root}
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(root, "pic.png"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.readFile(ctx, map[string]interface{}{"path": "pic.png"}); err == nil {
		t.Fatal("read_file must reject image files")
	}
}
