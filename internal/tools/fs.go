package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const maxReadBytes = 4 << 20 // 4 MiB read cap

// fsTools provides filesystem access confined to a workspace root.
type fsTools struct{ root string }

// resolve maps a caller-supplied path to an absolute path guaranteed to be
// within the workspace root, rejecting traversal outside it.
func (f *fsTools) resolve(p string) (string, error) {
	var abs string
	if p == "" {
		abs = f.root
	} else if filepath.IsAbs(p) {
		abs = filepath.Clean(p)
	} else {
		abs = filepath.Clean(filepath.Join(f.root, p))
	}
	rel, err := filepath.Rel(f.root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the workspace root", p)
	}
	return abs, nil
}

func (f *fsTools) register(r *Registry) {
	r.Register(&Tool{
		Name:        "read_file",
		Description: "Read a UTF-8 text file within the workspace.",
		Schema:      obj(map[string]interface{}{"path": strProp("Path relative to the workspace root")}, "path"),
		ReadOnly:    true,
		Handler:     f.read,
	})
	r.Register(&Tool{
		Name:        "write_file",
		Description: "Create or overwrite a file within the workspace.",
		Schema: obj(map[string]interface{}{
			"path":    strProp("Path relative to the workspace root"),
			"content": strProp("File content"),
		}, "path", "content"),
		Handler: f.write,
	})
	r.Register(&Tool{
		Name:        "list_directory",
		Description: "List entries of a directory within the workspace.",
		Schema:      obj(map[string]interface{}{"path": strProp("Directory path (default: workspace root)")}),
		ReadOnly:    true,
		Handler:     f.list,
	})
	r.Register(&Tool{
		Name:        "edit_file",
		Description: "Replace an exact substring in a file. Set replace_all to replace every occurrence.",
		Schema: obj(map[string]interface{}{
			"path":        strProp("Path relative to the workspace root"),
			"old_string":  strProp("Exact text to find"),
			"new_string":  strProp("Replacement text"),
			"replace_all": map[string]interface{}{"type": "boolean", "description": "Replace all occurrences"},
		}, "path", "old_string", "new_string"),
		Handler: f.edit,
	})
	r.Register(&Tool{
		Name:        "make_directory",
		Description: "Create a directory (and parents) within the workspace.",
		Schema:      obj(map[string]interface{}{"path": strProp("Directory path")}, "path"),
		Handler:     f.mkdir,
	})
	r.Register(&Tool{
		Name:        "delete_file",
		Description: "Delete a file, or a directory when recursive is true.",
		Schema: obj(map[string]interface{}{
			"path":      strProp("Path to delete"),
			"recursive": map[string]interface{}{"type": "boolean", "description": "Delete directories recursively"},
		}, "path"),
		Handler: f.del,
	})
	r.Register(&Tool{
		Name:        "move_file",
		Description: "Move or rename a file/directory within the workspace.",
		Schema: obj(map[string]interface{}{
			"source":      strProp("Source path"),
			"destination": strProp("Destination path"),
		}, "source", "destination"),
		Handler: f.move,
	})
	r.Register(&Tool{
		Name:        "path_info",
		Description: "Report whether a path exists and its type/size.",
		Schema:      obj(map[string]interface{}{"path": strProp("Path to inspect")}, "path"),
		ReadOnly:    true,
		Handler:     f.info,
	})
}

func (f *fsTools) read(_ context.Context, a map[string]interface{}) (string, error) {
	p, err := f.resolve(argString(a, "path"))
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(p)
	if err != nil {
		return "", err
	}
	if fi.Size() > maxReadBytes {
		return "", fmt.Errorf("file too large (%d bytes, max %d)", fi.Size(), maxReadBytes)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (f *fsTools) write(_ context.Context, a map[string]interface{}) (string, error) {
	p, err := f.resolve(argString(a, "path"))
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	content := argString(a, "content")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(content), p), nil
}

func (f *fsTools) list(_ context.Context, a map[string]interface{}) (string, error) {
	p, err := f.resolve(argString(a, "path"))
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(p)
	if err != nil {
		return "", err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "(empty)", nil
	}
	return strings.Join(names, "\n"), nil
}

func (f *fsTools) edit(_ context.Context, a map[string]interface{}) (string, error) {
	p, err := f.resolve(argString(a, "path"))
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	old, nw := argString(a, "old_string"), argString(a, "new_string")
	content := string(b)
	n := strings.Count(content, old)
	if n == 0 {
		return "", fmt.Errorf("old_string not found")
	}
	if n > 1 && !argBool(a, "replace_all", false) {
		return "", fmt.Errorf("old_string is not unique (%d matches); set replace_all or add context", n)
	}
	if argBool(a, "replace_all", false) {
		content = strings.ReplaceAll(content, old, nw)
	} else {
		content = strings.Replace(content, old, nw, 1)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("replaced %d occurrence(s) in %s", n, p), nil
}

func (f *fsTools) mkdir(_ context.Context, a map[string]interface{}) (string, error) {
	p, err := f.resolve(argString(a, "path"))
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(p, 0o755); err != nil {
		return "", err
	}
	return "created " + p, nil
}

func (f *fsTools) del(_ context.Context, a map[string]interface{}) (string, error) {
	p, err := f.resolve(argString(a, "path"))
	if err != nil {
		return "", err
	}
	if p == f.root {
		return "", fmt.Errorf("refusing to delete the workspace root")
	}
	if argBool(a, "recursive", false) {
		err = os.RemoveAll(p)
	} else {
		err = os.Remove(p)
	}
	if err != nil {
		return "", err
	}
	return "deleted " + p, nil
}

func (f *fsTools) move(_ context.Context, a map[string]interface{}) (string, error) {
	src, err := f.resolve(argString(a, "source"))
	if err != nil {
		return "", err
	}
	dst, err := f.resolve(argString(a, "destination"))
	if err != nil {
		return "", err
	}
	if err := os.Rename(src, dst); err != nil {
		return "", err
	}
	return fmt.Sprintf("moved %s -> %s", src, dst), nil
}

func (f *fsTools) info(_ context.Context, a map[string]interface{}) (string, error) {
	p, err := f.resolve(argString(a, "path"))
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(p)
	if os.IsNotExist(err) {
		return fmt.Sprintf("%s: does not exist", p), nil
	}
	if err != nil {
		return "", err
	}
	kind := "file"
	if fi.IsDir() {
		kind = "directory"
	}
	return fmt.Sprintf("%s: %s, %d bytes, mode %s", p, kind, fi.Size(), fi.Mode()), nil
}
