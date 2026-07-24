package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// fsTools provides filesystem access confined to a workspace root, ported from
// goclaw's FileTool. All operations resolve paths within root (following
// symlinks) and reject escapes. llamaURL points at an OpenAI-compatible vision
// endpoint used by analyze_image.
type fsTools struct {
	root     string
	llamaURL string
	cache    string // base dir for generated scratch (images); falls back to root
}

// imagesDir is where analyze_image saves copies and list_images looks. Uses the
// cache dir when set so generated images don't clutter the (whole-home) root.
func (f *fsTools) imagesDir() string {
	if f.cache != "" {
		return filepath.Join(f.cache, "images")
	}
	return filepath.Join(f.root, "images")
}

// resolve validates that path stays within root even after symlinks are
// followed, returning the symlink-resolved absolute path. Mirrors
// util.SafeResolve in goclaw.
func (f *fsTools) resolve(path string) (string, error) {
	rootAbs, err := filepath.Abs(f.root)
	if err != nil {
		return "", fmt.Errorf("invalid root directory: %s", f.root)
	}
	rootReal := rootAbs
	if r, err := filepath.EvalSymlinks(rootAbs); err == nil {
		rootReal = r
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(rootAbs, path)
	}
	target, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("invalid path: %s", path)
	}
	targetReal, err := evalSymlinksAllowMissing(target)
	if err != nil {
		return "", fmt.Errorf("invalid path: %s", path)
	}
	if !isWithin(rootReal, targetReal) {
		return "", fmt.Errorf("path escape detected: %s is outside %s", path, f.root)
	}
	return targetReal, nil
}

// isWithin reports whether target lies inside (or equals) root, comparing on
// path-segment boundaries rather than raw string prefix.
func isWithin(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// evalSymlinksAllowMissing resolves symlinks in an absolute path, tolerating
// trailing components that do not yet exist (write/create targets).
func evalSymlinksAllowMissing(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	parent := filepath.Dir(path)
	if parent == path {
		return path, nil
	}
	resolvedParent, err := evalSymlinksAllowMissing(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolvedParent, filepath.Base(path)), nil
}

func (f *fsTools) register(r *Registry) {
	r.Register(&Tool{
		Name:        "list_directory",
		Description: "List the contents of a directory.",
		Schema:      obj(map[string]interface{}{"path": strProp("Path to the directory to list (relative or absolute)")}, "path"),
		ReadOnly:    true,
		Handler:     f.listDirectory,
	})
	r.Register(&Tool{
		Name:        "read_file",
		Description: "Read a text file. Supports offset/limit for partial reads of large files.",
		Schema: obj(map[string]interface{}{
			"path":      strProp("Path to the file to read (relative or absolute)"),
			"offset":    map[string]interface{}{"type": "integer", "description": "Line number to start reading from (1-indexed, default 1)"},
			"limit":     map[string]interface{}{"type": "integer", "description": "Maximum number of lines to read (default: read all, capped at 20000 chars)"},
			"max_chars": map[string]interface{}{"type": "integer", "description": "Maximum characters to return (truncates when exceeded, default 20000)"},
		}, "path"),
		ReadOnly: true,
		Handler:  f.readFile,
	})
	r.Register(&Tool{
		Name:        "write_file",
		Description: "Write content to a file, creating it or overwriting.",
		Schema: obj(map[string]interface{}{
			"path":    strProp("Path to the file to write (relative or absolute)"),
			"content": strProp("Content to write to the file"),
		}, "path", "content"),
		Handler: f.writeFile,
	})
	r.Register(&Tool{
		Name:        "append_file",
		Description: "Append content to the end of a file, creating it (and parent directories) if it doesn't exist. Use instead of write_file when you want to add to a file rather than overwrite it.",
		Schema: obj(map[string]interface{}{
			"path":    strProp("Path to the file to append to (relative or absolute)"),
			"content": strProp("Content to append to the file"),
		}, "path", "content"),
		Handler: f.appendFile,
	})
	r.Register(&Tool{
		Name:        "touch",
		Description: "Create an empty file if it doesn't exist (creating parent directories as needed), or update the timestamps of an existing file or directory to now.",
		Schema:      obj(map[string]interface{}{"path": strProp("Path to touch (relative or absolute)")}, "path"),
		Handler:     f.touch,
	})
	r.Register(&Tool{
		Name:        "path_info",
		Description: "Inspect a path: its directory, name, stem, and extension, plus whether it exists and its type (file/directory/symlink). Does not error on missing paths — use it to check existence or build paths.",
		Schema:      obj(map[string]interface{}{"path": strProp("Path to inspect (relative or absolute)")}, "path"),
		ReadOnly:    true,
		Handler:     f.pathInfo,
	})
	r.Register(&Tool{
		Name:        "edit_file",
		Description: "Replace exact text in a file. old_text must be unique unless replace_all is true.",
		Schema: obj(map[string]interface{}{
			"path":        strProp("Path to the file to edit (relative or absolute)"),
			"old_text":    strProp("Exact text to find and replace. Must be unique in the file unless replace_all is true."),
			"new_text":    strProp("Replacement text"),
			"replace_all": map[string]interface{}{"type": "boolean", "description": "Replace every occurrence of old_text instead of requiring it to be unique (default false)."},
		}, "path", "old_text", "new_text"),
		Handler: f.editFile,
	})
	r.Register(&Tool{
		Name:        "delete_file",
		Description: "Delete a file, or a directory (with recursive=true) and its contents.",
		Schema: obj(map[string]interface{}{
			"path":      strProp("Path to the file or directory to delete (relative or absolute)"),
			"recursive": map[string]interface{}{"type": "boolean", "description": "Required to delete a directory and its contents (default false). Ignored for files."},
		}, "path"),
		Handler: f.deleteFile,
	})
	r.Register(&Tool{
		Name:        "create_directory",
		Description: "Create a directory (and parent directories if needed).",
		Schema: obj(map[string]interface{}{
			"path": strProp("Path to the directory to create (relative or absolute). Parent directories are created automatically."),
		}, "path"),
		Handler: f.createDirectory,
	})
	r.Register(&Tool{
		Name:        "move_file",
		Description: "Move or rename a file or directory. Works across directories and filesystems.",
		Schema: obj(map[string]interface{}{
			"old_path": strProp("Current path of the file or directory (relative or absolute)"),
			"new_path": strProp("New path for the file or directory (relative or absolute). Can be in a different directory to move, or same directory with different name to rename."),
		}, "old_path", "new_path"),
		Handler: f.moveFile,
	})
	r.Register(&Tool{
		Name:        "copy_file",
		Description: "Copy a file or directory (recursively) to a new location, preserving permissions.",
		Schema: obj(map[string]interface{}{
			"source_path": strProp("Path of the file or directory to copy (relative or absolute). Directories are copied recursively."),
			"dest_path":   strProp("Destination path for the copy (relative or absolute)"),
		}, "source_path", "dest_path"),
		Handler: f.copyFile,
	})
	r.Register(&Tool{
		Name:        "rename_file",
		Description: "Rename a file or directory within the same directory. Use move_file to change directories.",
		Schema: obj(map[string]interface{}{
			"old_path": strProp("Current path of the file or directory (relative or absolute)"),
			"new_path": strProp("New path for the file or directory (same directory, different name)"),
		}, "old_path", "new_path"),
		Handler: f.renameFile,
	})
	r.Register(&Tool{
		Name:        "analyze_image",
		Description: "Analyze an image with the multimodal model. Saves a copy to workspace/images/.",
		Schema: obj(map[string]interface{}{
			"path":   strProp("Path to the image file to analyze (relative or absolute). Images are automatically saved to workspace/images/ with a timestamped filename. Use 'list_images' to see all saved images."),
			"prompt": strProp("Prompt describing what to look for in the image (default: 'Describe this image in detail.')"),
		}, "path"),
		Handler: f.analyzeImage,
	})
	r.Register(&Tool{
		Name:        "list_images",
		Description: "List all images saved in workspace/images/.",
		Schema:      obj(map[string]interface{}{}),
		ReadOnly:    true,
		Handler:     f.listImages,
	})
}

func (f *fsTools) readFile(_ context.Context, a map[string]interface{}) (string, error) {
	path := argString(a, "path")
	if path == "" {
		return "", errors.New("path is required and must be a string")
	}
	resolved, err := f.resolve(path)
	if err != nil {
		return "", err
	}

	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".bmp", ".webp":
		return "", fmt.Errorf("%s is an image file. Use the 'analyze_image' tool to view it instead of read_file", path)
	}

	data, err := os.ReadFile(resolved)
	if err != nil {
		return "", fmt.Errorf("reading file: %w", err)
	}

	// Detect binary files by checking for null bytes (>10% => binary).
	const binaryThreshold = 0.1
	if len(data) > 0 {
		nullCount := 0
		for _, b := range data {
			if b == 0 {
				nullCount++
			}
		}
		if float64(nullCount)/float64(len(data)) > binaryThreshold {
			return "", fmt.Errorf("%s appears to be a binary file. Only text files can be read with read_file", path)
		}
	}

	offset := argInt(a, "offset", 1)
	if offset < 1 {
		offset = 1
	}
	limit := argInt(a, "limit", 0)

	lines := strings.Split(string(data), "\n")
	totalLines := len(lines)

	start := offset - 1
	if start < 0 {
		start = 0
	}
	if start >= totalLines {
		return fmt.Sprintf("File has %d lines, offset %d is beyond the end", totalLines, offset), nil
	}
	end := totalLines
	if limit > 0 && start+limit < end {
		end = start + limit
	}

	content := strings.Join(lines[start:end], "\n")

	maxChars := argInt(a, "max_chars", 20000)
	if maxChars <= 0 {
		maxChars = 20000
	}
	if truncated, dropped := truncateRunes(content, maxChars); dropped > 0 {
		content = truncated + fmt.Sprintf("\n\n... (truncated, %d more chars beyond the %d-char limit)", dropped, maxChars)
	}

	var info []string
	if offset > 1 || limit > 0 {
		lineNum := start + 1
		lineEnd := len(lines[start:end])
		info = append(info, fmt.Sprintf("Lines %d-%d of %d", lineNum, lineNum+lineEnd-1, totalLines))
	}
	if totalLines > 20000 && offset == 1 && limit == 0 {
		info = append(info, fmt.Sprintf("File has %d lines total (showing first 20000 chars)", totalLines))
	}
	if len(info) > 0 {
		content = content + "\n\n[" + strings.Join(info, ", ") + "]"
	}
	return content, nil
}

func (f *fsTools) writeFile(_ context.Context, a map[string]interface{}) (string, error) {
	path := argString(a, "path")
	if path == "" {
		return "", errors.New("path is required and must be a string")
	}
	if _, ok := a["content"]; !ok {
		return "", errors.New("content is required and must be a string")
	}
	content := argString(a, "content")
	resolved, err := f.resolve(path)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(resolved), 0755); err != nil {
		return "", fmt.Errorf("creating directory: %w", err)
	}
	if err := os.WriteFile(resolved, []byte(content), 0644); err != nil {
		return "", fmt.Errorf("writing file: %w", err)
	}
	return fmt.Sprintf("Successfully wrote %d bytes to %s", len(content), path), nil
}

func (f *fsTools) appendFile(_ context.Context, a map[string]interface{}) (string, error) {
	path := argString(a, "path")
	if path == "" {
		return "", errors.New("path is required and must be a string")
	}
	if _, ok := a["content"]; !ok {
		return "", errors.New("content is required and must be a string")
	}
	content := argString(a, "content")
	resolved, err := f.resolve(path)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(resolved), 0755); err != nil {
		return "", fmt.Errorf("creating directory: %w", err)
	}
	fh, err := os.OpenFile(resolved, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return "", fmt.Errorf("opening file: %w", err)
	}
	if _, err := fh.WriteString(content); err != nil {
		fh.Close()
		return "", fmt.Errorf("appending to file: %w", err)
	}
	if err := fh.Close(); err != nil {
		return "", fmt.Errorf("closing file: %w", err)
	}
	return fmt.Sprintf("Appended %d bytes to %s", len(content), path), nil
}

func (f *fsTools) touch(_ context.Context, a map[string]interface{}) (string, error) {
	path := argString(a, "path")
	if path == "" {
		return "", errors.New("path is required and must be a string")
	}
	resolved, err := f.resolve(path)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(resolved); err == nil {
		now := time.Now()
		if err := os.Chtimes(resolved, now, now); err != nil {
			return "", fmt.Errorf("updating timestamps: %w", err)
		}
		return fmt.Sprintf("Updated timestamps of %s", path), nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(resolved), 0755); err != nil {
		return "", fmt.Errorf("creating directory: %w", err)
	}
	fh, err := os.OpenFile(resolved, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return "", fmt.Errorf("creating file: %w", err)
	}
	if err := fh.Close(); err != nil {
		return "", fmt.Errorf("closing file: %w", err)
	}
	return fmt.Sprintf("Created empty file %s", path), nil
}

func (f *fsTools) pathInfo(_ context.Context, a map[string]interface{}) (string, error) {
	path := argString(a, "path")
	if path == "" {
		return "", errors.New("path is required and must be a string")
	}
	resolved, err := f.resolve(path)
	if err != nil {
		return "", err
	}
	base := filepath.Base(resolved)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	if ext == "" {
		ext = "(none)"
	}
	rootReal := f.root
	if r, err := filepath.Abs(f.root); err == nil {
		rootReal = r
		if rr, err := filepath.EvalSymlinks(r); err == nil {
			rootReal = rr
		}
	}
	rel, relErr := filepath.Rel(rootReal, resolved)
	if relErr != nil {
		rel = resolved
	}
	var typ, extra string
	if info, err := os.Lstat(resolved); err != nil {
		if os.IsNotExist(err) {
			typ = "does not exist"
		} else {
			typ = "unknown (" + err.Error() + ")"
		}
	} else {
		switch {
		case info.IsDir():
			typ = "directory"
		case info.Mode()&os.ModeSymlink != 0:
			typ = "symlink"
		case info.Mode().IsRegular():
			typ = "file"
			extra = fmt.Sprintf("\n  Size: %s", formatSize(info.Size()))
		default:
			typ = "other"
		}
	}
	return fmt.Sprintf("Path: %s\n  Absolute: %s\n  Workspace-relative: %s\n  Directory: %s\n  Name: %s\n  Stem: %s\n  Extension: %s\n  Type: %s%s",
		path, resolved, rel, filepath.Dir(resolved), base, stem, ext, typ, extra), nil
}

func (f *fsTools) listDirectory(_ context.Context, a map[string]interface{}) (string, error) {
	path := argString(a, "path")
	if path == "" {
		return "", errors.New("path is required and must be a string")
	}
	resolved, err := f.resolve(path)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(resolved)
	if err != nil {
		return "", fmt.Errorf("reading directory: %w", err)
	}
	relDir, err := filepath.Rel(f.root, resolved)
	if err != nil {
		relDir = "."
	}
	var lines []string
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			lines = append(lines, fmt.Sprintf("????? ????  %s", e.Name()))
			continue
		}
		mode := info.Mode()
		dir := ' '
		if mode.IsDir() {
			dir = '/'
		}
		name := e.Name()
		if relDir != "." {
			name = filepath.ToSlash(filepath.Join(relDir, name))
		}
		lines = append(lines, fmt.Sprintf("%c%04o %8d  %s", dir, mode.Perm()&0777, info.Size(), name))
	}
	return strings.Join(lines, "\n"), nil
}

func (f *fsTools) editFile(_ context.Context, a map[string]interface{}) (string, error) {
	path := argString(a, "path")
	if path == "" {
		return "", errors.New("path is required and must be a string")
	}
	oldText := argString(a, "old_text")
	if oldText == "" {
		return "", errors.New("old_text is required and must be a non-empty string")
	}
	if _, ok := a["new_text"]; !ok {
		return "", errors.New("new_text is required and must be a string")
	}
	newText := argString(a, "new_text")
	replaceAll := argBool(a, "replace_all", false)

	resolved, err := f.resolve(path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return "", fmt.Errorf("reading file: %w", err)
	}
	content := string(data)
	count := strings.Count(content, oldText)
	if count == 0 {
		return "", fmt.Errorf("old_text not found in file %s (0 occurrences)", path)
	}
	if count > 1 && !replaceAll {
		return "", fmt.Errorf("old_text matches %d locations. Add more context to make it unique, or set replace_all=true to replace every occurrence", count)
	}
	newContent := content
	replacements := 1
	if replaceAll {
		newContent = strings.ReplaceAll(content, oldText, newText)
		replacements = count
	} else {
		newContent = strings.Replace(content, oldText, newText, 1)
	}
	if err := os.WriteFile(resolved, []byte(newContent), 0644); err != nil {
		return "", fmt.Errorf("writing file: %w", err)
	}
	plural := "replacement"
	if replacements != 1 {
		plural = "replacements"
	}
	return fmt.Sprintf("Successfully edited %s (%d %s)", path, replacements, plural), nil
}

func (f *fsTools) deleteFile(_ context.Context, a map[string]interface{}) (string, error) {
	path := argString(a, "path")
	if path == "" {
		return "", errors.New("path is required and must be a string")
	}
	recursive := argBool(a, "recursive", false)
	resolved, err := f.resolve(path)
	if err != nil {
		return "", err
	}
	if root, err := filepath.Abs(f.root); err == nil && resolved == root {
		return "", errors.New("refusing to delete the workspace root")
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		if !recursive {
			return "", fmt.Errorf("%s is a directory. Set recursive=true to delete it and its contents", path)
		}
		if err := os.RemoveAll(resolved); err != nil {
			return "", fmt.Errorf("deleting directory: %w", err)
		}
		return fmt.Sprintf("Successfully deleted directory %s", path), nil
	}
	if err := os.Remove(resolved); err != nil {
		return "", fmt.Errorf("deleting file: %w", err)
	}
	return fmt.Sprintf("Successfully deleted %s", path), nil
}

func (f *fsTools) createDirectory(_ context.Context, a map[string]interface{}) (string, error) {
	path := argString(a, "path")
	if path == "" {
		return "", errors.New("path is required and must be a string")
	}
	resolved, err := f.resolve(path)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(resolved, 0755); err != nil {
		return "", fmt.Errorf("creating directory: %w", err)
	}
	return fmt.Sprintf("Created directory: %s (resolved: %s)", path, resolved), nil
}

func (f *fsTools) moveFile(_ context.Context, a map[string]interface{}) (string, error) {
	oldPath := argString(a, "old_path")
	if oldPath == "" {
		return "", errors.New("old_path is required and must be a string")
	}
	newPath := argString(a, "new_path")
	if newPath == "" {
		return "", errors.New("new_path is required and must be a string")
	}
	oldResolved, err := f.resolve(oldPath)
	if err != nil {
		return "", err
	}
	newResolved, err := f.resolve(newPath)
	if err != nil {
		return "", err
	}
	if err := os.Rename(oldResolved, newResolved); err != nil {
		// Fall back to copy+delete across filesystem boundaries (EXDEV).
		var linkErr *os.LinkError
		if errors.As(err, &linkErr) && errors.Is(linkErr.Err, syscall.EXDEV) {
			if cerr := copyPath(oldResolved, newResolved); cerr != nil {
				return "", fmt.Errorf("moving (cross-device copy): %w", cerr)
			}
			if rerr := os.RemoveAll(oldResolved); rerr != nil {
				return "", fmt.Errorf("moving (removing source after copy): %w", rerr)
			}
		} else {
			return "", fmt.Errorf("moving: %w", err)
		}
	}
	return fmt.Sprintf("Moved %s -> %s (resolved: %s -> %s)", oldPath, newPath, oldResolved, newResolved), nil
}

func (f *fsTools) copyFile(_ context.Context, a map[string]interface{}) (string, error) {
	srcPath := argString(a, "source_path")
	if srcPath == "" {
		return "", errors.New("source_path is required and must be a string")
	}
	dstPath := argString(a, "dest_path")
	if dstPath == "" {
		return "", errors.New("dest_path is required and must be a string")
	}
	srcResolved, err := f.resolve(srcPath)
	if err != nil {
		return "", err
	}
	dstResolved, err := f.resolve(dstPath)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(srcResolved); err != nil {
		return "", fmt.Errorf("reading source: %w", err)
	}
	if err := copyPath(srcResolved, dstResolved); err != nil {
		return "", fmt.Errorf("copying: %w", err)
	}
	return fmt.Sprintf("Copied %s -> %s", srcPath, dstPath), nil
}

func (f *fsTools) renameFile(_ context.Context, a map[string]interface{}) (string, error) {
	oldPath := argString(a, "old_path")
	if oldPath == "" {
		return "", errors.New("old_path is required and must be a string")
	}
	newPath := argString(a, "new_path")
	if newPath == "" {
		return "", errors.New("new_path is required and must be a string")
	}
	oldResolved, err := f.resolve(oldPath)
	if err != nil {
		return "", err
	}
	newResolved, err := f.resolve(newPath)
	if err != nil {
		return "", err
	}
	if filepath.Dir(oldResolved) != filepath.Dir(newResolved) {
		return "", errors.New("rename_file only works within the same directory. Use move_file to change directories")
	}
	if err := os.Rename(oldResolved, newResolved); err != nil {
		return "", fmt.Errorf("renaming: %w", err)
	}
	return fmt.Sprintf("Renamed %s -> %s", oldPath, newPath), nil
}

func (f *fsTools) listImages(_ context.Context, a map[string]interface{}) (string, error) {
	imagesDir := f.imagesDir()
	entries, err := os.ReadDir(imagesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "No images directory found. Images are saved here when you use the analyze_image tool or when users send photos.", nil
		}
		return "", fmt.Errorf("reading images directory: %w", err)
	}
	if len(entries) == 0 {
		return "No images saved yet.", nil
	}
	var lines []string
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			lines = append(lines, fmt.Sprintf("?       %s", e.Name()))
			continue
		}
		name := filepath.ToSlash(filepath.Join("images", e.Name()))
		lines = append(lines, fmt.Sprintf("%8s  %s", formatSize(info.Size()), name))
	}
	return fmt.Sprintf("Saved images (%d):\n%s", len(entries), strings.Join(lines, "\n")), nil
}

func (f *fsTools) analyzeImage(ctx context.Context, a map[string]interface{}) (string, error) {
	path := argString(a, "path")
	if path == "" {
		return "", errors.New("path is required and must be a string")
	}
	prompt := argString(a, "prompt")
	if prompt == "" {
		prompt = "Describe this image in detail."
	}
	resolved, err := f.resolve(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory, not an image", path)
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return "", fmt.Errorf("reading file: %w", err)
	}

	mimeType := "image/jpeg"
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".png":
		mimeType = "image/png"
	case ".webp":
		mimeType = "image/webp"
	case ".bmp":
		mimeType = "image/bmp"
	case ".gif":
		mimeType = "image/gif"
	}
	dataURL := fmt.Sprintf("data:%s;base64,%s", mimeType, base64.StdEncoding.EncodeToString(data))

	// Save a timestamped copy to the images dir.
	imagesDir := f.imagesDir()
	_ = os.MkdirAll(imagesDir, 0755)
	baseName := filepath.Base(path)
	nameWithoutExt := strings.TrimSuffix(baseName, ext)
	savedName := fmt.Sprintf("%s_%s%s", nameWithoutExt, time.Now().Format("20060102-150405"), ext)
	savedPath := filepath.Join(imagesDir, savedName)
	_ = os.WriteFile(savedPath, data, 0644)

	if f.llamaURL != "" {
		description, err := f.visionAnalyze(ctx, dataURL, prompt)
		if err != nil {
			return "", fmt.Errorf("analyzing image: %w", err)
		}
		return fmt.Sprintf("%s\n\nSaved to %s (see list_images)", description, savedPath), nil
	}
	// Fallback: return the base64 data URL.
	return dataURL, nil
}

// visionAnalyze sends an image data URL and prompt to the OpenAI-compatible
// chat/completions endpoint at llamaURL and returns the model's text response.
// Mirrors llama.Client.AnalyzeImage.
func (f *fsTools) visionAnalyze(ctx context.Context, imageDataURL, prompt string) (string, error) {
	reqBody := map[string]interface{}{
		"stream":     false,
		"max_tokens": 1024,
		"messages": []interface{}{
			map[string]interface{}{
				"role": "user",
				"content": []interface{}{
					map[string]interface{}{"type": "text", "text": prompt},
					map[string]interface{}{"type": "image_url", "image_url": map[string]interface{}{"url": imageDataURL}},
				},
			},
		},
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshaling request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", f.llamaURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 600 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("llama.cpp API error (status %d): %s", resp.StatusCode, string(respBody))
	}
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decoding response: %w", err)
	}
	if len(result.Choices) == 0 {
		return "", errors.New("empty response from llama.cpp")
	}
	return result.Choices[0].Message.Content, nil
}

// copyPath copies src to dst, recursing into directories and preserving file
// permission bits. Parent directories of dst are created as needed.
func copyPath(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		if err := os.MkdirAll(dst, info.Mode().Perm()); err != nil {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := copyPath(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// formatSize renders a byte count as a human-readable string.
func formatSize(size int64) string {
	switch {
	case size >= 1024*1024:
		return fmt.Sprintf("%.1fMB", float64(size)/(1024*1024))
	case size >= 1024:
		return fmt.Sprintf("%.1fKB", float64(size)/1024)
	default:
		return fmt.Sprintf("%dB", size)
	}
}

// truncateRunes truncates s to at most max runes without splitting a rune,
// returning the truncated string and the number of runes dropped.
func truncateRunes(s string, max int) (string, int) {
	runes := []rune(s)
	if len(runes) <= max {
		return s, 0
	}
	return string(runes[:max]), len(runes) - max
}
