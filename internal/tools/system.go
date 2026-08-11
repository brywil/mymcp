package tools

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/brywil/mymcp/internal/mcp"
)

// -----------------------------------------------------------------------------
// sysTools: filesystem/host utilities confined to a workspace root.
// -----------------------------------------------------------------------------

// sysTools provides system-level utility tools (grep, file info, process/disk
// inspection, find). File-touching tools are confined to the workspace root.
type sysTools struct{ root string }

// resolve maps a caller path to an absolute path within the workspace root.
func (s *sysTools) resolve(p string) (string, error) {
	var abs string
	if p == "" {
		abs = s.root
	} else if filepath.IsAbs(p) {
		abs = filepath.Clean(p)
	} else {
		abs = filepath.Clean(filepath.Join(s.root, p))
	}
	rel, err := filepath.Rel(s.root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the workspace root", p)
	}
	return abs, nil
}

func (s *sysTools) register(r *Registry) {
	r.Register(&Tool{
		Name:        "grep",
		Description: "Search file contents for a regex pattern. Case-sensitive by default (Linux). Use case_insensitive=true for case-insensitive.",
		Schema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"pattern":          strProp("Regular expression pattern to search for"),
				"path":             strProp("File or directory to search (default: current directory)"),
				"recursive":        map[string]interface{}{"type": "boolean", "description": "Recursively search directories (default: true for directories, false for files)"},
				"case_insensitive": map[string]interface{}{"type": "boolean", "description": "Case-insensitive matching (default: false — case-sensitive, matching Linux semantics)"},
				"max_results":      map[string]interface{}{"type": "integer", "description": "Maximum number of results to return (default: 100)"},
			},
			"required": []string{"pattern"},
		},
		ReadOnly: true,
		Handler:  s.grep,
	})
	r.Register(&Tool{
		Name:        "count_lines",
		Description: "Count lines, words, and bytes in a file.",
		Schema:      obj(map[string]interface{}{"path": strProp("Path to the file (relative or absolute)")}, "path"),
		ReadOnly:    true,
		Handler:     s.countLines,
	})
	r.Register(&Tool{
		Name:        "list_processes",
		Description: "List running processes. Optionally filter by name.",
		Schema:      obj(map[string]interface{}{"filter": strProp("Filter processes by name/pattern (optional)")}),
		ReadOnly:    true,
		Handler:     s.listProcesses,
	})
	r.Register(&Tool{
		Name:        "check_disk_space",
		Description: "Check disk space usage for a path.",
		Schema:      obj(map[string]interface{}{"path": strProp("Path to check (default: /)")}),
		ReadOnly:    true,
		Handler:     s.checkDiskSpace,
	})
	r.Register(&Tool{
		Name:        "file_info",
		Description: "Get detailed file metadata including size, permissions, owner, and modification time.",
		Schema:      obj(map[string]interface{}{"path": strProp("Path to the file (relative or absolute)")}, "path"),
		ReadOnly:    true,
		Handler:     s.fileInfo,
	})
	r.Register(&Tool{
		Name:        "list_env_vars",
		Description: "List environment variables. Optionally filter by prefix.",
		Schema:      obj(map[string]interface{}{"prefix": strProp("Filter environment variables by prefix (optional)")}),
		ReadOnly:    true,
		Handler:     s.listEnvVars,
	})
	r.Register(&Tool{
		Name:        "find_files",
		Description: "Search for files matching a glob pattern within the workspace.",
		Schema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"pattern":   strProp("Glob pattern to match files (e.g., '*.go', 'memory/*.md', '**/*.txt'). Supports ** for recursive matching."),
				"directory": strProp("Directory to search within (relative or absolute). Defaults to workspace root if not specified."),
			},
			"required": []string{"pattern"},
		},
		ReadOnly: true,
		Handler:  s.findFiles,
	})
	r.Register(&Tool{
		Name:        "sed",
		Description: "Perform regex-based text substitution in a file. Case-sensitive by default (Linux). Use case_insensitive=true for case-insensitive.",
		Schema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path":             strProp("Path to the file to edit (relative or absolute)"),
				"pattern":          strProp("Regular expression pattern to match"),
				"replacement":      strProp("Replacement text"),
				"global":           map[string]interface{}{"type": "boolean", "description": "Replace all occurrences (default: false, only first per line)"},
				"case_insensitive": map[string]interface{}{"type": "boolean", "description": "Case-insensitive matching (default: false — case-sensitive, matching Linux semantics)"},
			},
			"required": []string{"path", "pattern", "replacement"},
		},
		ReadOnly: false,
		Handler:  s.sed,
	})
	r.Register(&Tool{
		Name:        "awk",
		Description: "Perform awk-like text processing on a file. Supports field access ($1, $2), pattern matching, and print statements.",
		Schema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path":            strProp("Path to the file to process (relative or absolute)"),
				"program":         strProp("Awk-like program (e.g., '/error/ { print $0 }' or '{ print $1, $2 }')"),
				"field_separator": strProp("Field separator (default: whitespace)"),
			},
			"required": []string{"path", "program"},
		},
		ReadOnly: true,
		Handler:  s.awk,
	})
}

// sed performs regex-based text substitution on a file.
func (s *sysTools) sed(_ context.Context, args map[string]interface{}) (string, error) {
	path := argString(args, "path")
	if path == "" {
		return "", errors.New("path is required and must be a string")
	}
	pattern := argString(args, "pattern")
	if pattern == "" {
		return "", errors.New("pattern is required and must be a string")
	}
	replacement := argString(args, "replacement")
	if replacement == "" {
		return "", errors.New("replacement is required and must be a string")
	}

	resolved, err := s.resolve(path)
	if err != nil {
		return "", err
	}

	data, err := os.ReadFile(resolved)
	if err != nil {
		return "", fmt.Errorf("reading file: %s", err.Error())
	}

	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("invalid pattern: %s", err.Error())
	}

	global := argBool(args, "global", false)
	caseInsensitive := argBool(args, "case_insensitive", false)

	lines := strings.Split(string(data), "\n")
	changed := 0
	for i, line := range lines {
		text := line
		original := line
		if caseInsensitive {
			text = strings.ToLower(text)
		}
		if global {
			text = re.ReplaceAllString(text, replacement)
		} else {
			if match := re.FindStringIndex(text); len(match) == 2 {
				text = text[:match[0]] + replacement + text[match[1]:]
			}
		}
		if text != original {
			changed++
			lines[i] = text
		}
	}

	if changed > 0 {
		newData := []byte(strings.Join(lines, "\n"))
		if err := os.WriteFile(resolved, newData, 0644); err != nil {
			return "", fmt.Errorf("writing file: %s", err.Error())
		}
	}

	return fmt.Sprintf("Substituted %d occurrence(s) in %s", changed, path), nil
}

// awk performs awk-like text processing on a file.
func (s *sysTools) awk(_ context.Context, args map[string]interface{}) (string, error) {
	path := argString(args, "path")
	if path == "" {
		return "", errors.New("path is required and must be a string")
	}
	program := argString(args, "program")
	if program == "" {
		return "", errors.New("program is required and must be a string")
	}

	resolved, err := s.resolve(path)
	if err != nil {
		return "", err
	}

	data, err := os.ReadFile(resolved)
	if err != nil {
		return "", fmt.Errorf("reading file: %s", err.Error())
	}

	lines := strings.Split(string(data), "\n")

	var output strings.Builder
	fieldSep := " "
	if fs := argString(args, "field_separator"); fs != "" {
		fieldSep = fs
	}

	for _, line := range lines {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if s.awkMatches(line, fields, program) {
			result := s.awkExecute(line, fields, program, fieldSep)
			if result != "" {
				output.WriteString(result + "\n")
			}
		}
	}

	if output.Len() == 0 {
		return "(no output)", nil
	}

	result := output.String()
	if len(result) > 20000 {
		result = result[:20000] + "\n\n... (truncated)"
	}
	return result, nil
}

// awkMatches checks if a line matches the awk program patterns.
func (s *sysTools) awkMatches(line string, fields []string, program string) bool {
	patternPart := program
	if idx := strings.Index(program, "{"); idx != -1 {
		patternPart = program[:idx]
	}

	if strings.Contains(patternPart, "BEGIN") || strings.Contains(patternPart, "END") {
		return true
	}

	if strings.HasPrefix(strings.TrimSpace(patternPart), "/") {
		reEnd := strings.Index(patternPart[1:], "/")
		if reEnd != -1 {
			pattern := patternPart[1 : reEnd+1]
			re, err := regexp.Compile(pattern)
			if err == nil && re.MatchString(line) {
				return true
			}
		}
	}

	if strings.Contains(patternPart, "$") {
		return s.awkFieldMatch(fields, patternPart)
	}

	return true
}

// awkFieldMatch checks field conditions in a pattern.
func (s *sysTools) awkFieldMatch(fields []string, pattern string) bool {
	if strings.Contains(pattern, "==") {
		parts := strings.SplitN(pattern, "==", 2)
		if len(parts) == 2 {
			fieldIdx, err := strconv.Atoi(strings.TrimSpace(parts[0][1:]))
			if err == nil && fieldIdx > 0 && fieldIdx <= len(fields) {
				value := strings.TrimSpace(parts[1])
				value = strings.Trim(value, "\"")
				return fields[fieldIdx-1] == value
			}
		}
	}
	if strings.Contains(pattern, "!=") {
		parts := strings.SplitN(pattern, "!=", 2)
		if len(parts) == 2 {
			fieldIdx, err := strconv.Atoi(strings.TrimSpace(parts[0][1:]))
			if err == nil && fieldIdx > 0 && fieldIdx <= len(fields) {
				value := strings.TrimSpace(parts[1])
				value = strings.Trim(value, "\"")
				return fields[fieldIdx-1] != value
			}
		}
	}
	return false
}

// awkExecute executes the action part of an awk program.
func (s *sysTools) awkExecute(line string, fields []string, program string, sep string) string {
	action := program
	if idx := strings.Index(program, "{"); idx != -1 {
		action = program[idx:]
	}

	if strings.Contains(action, "print") {
		printArgs := strings.TrimSuffix(strings.TrimPrefix(action, "{ print "), "}")
		printArgs = strings.TrimSpace(printArgs)

		if printArgs == "" || printArgs == "$0" {
			return line
		}

		var parts []string
		for _, part := range strings.Fields(printArgs) {
			if strings.HasPrefix(part, "$") {
				fieldIdx, err := strconv.Atoi(part[1:])
				if err == nil && fieldIdx > 0 && fieldIdx <= len(fields) {
					parts = append(parts, fields[fieldIdx-1])
				}
			} else {
				part = strings.Trim(part, "\"")
				parts = append(parts, part)
			}
		}
		return strings.Join(parts, sep)
	}

	if strings.Contains(action, "printf") {
		return line
	}

	return ""
}

func (s *sysTools) grep(_ context.Context, args map[string]interface{}) (string, error) {
	pattern := argString(args, "pattern")
	if pattern == "" {
		return "", errors.New("pattern is required and must be a string")
	}
	path := argString(args, "path")
	if path == "" {
		path = "."
	}
	resolved, err := s.resolve(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("invalid regex pattern: %s", err.Error())
	}
	recursive := argBool(args, "recursive", false)
	caseInsensitive := argBool(args, "case_insensitive", false)
	maxResults := argInt(args, "max_results", 100)
	if maxResults <= 0 {
		maxResults = 100
	}

	var results []string
	matchCount := 0

	if info.IsDir() {
		if !recursive {
			recursive = true
		}
		_ = recursive
		err = filepath.Walk(resolved, func(fp string, fi os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if fi.IsDir() {
				return nil
			}
			if strings.HasSuffix(fp, ".go") || strings.HasSuffix(fp, ".js") ||
				strings.HasSuffix(fp, ".ts") || strings.HasSuffix(fp, ".py") ||
				strings.HasSuffix(fp, ".md") || strings.HasSuffix(fp, ".json") ||
				strings.HasSuffix(fp, ".yaml") || strings.HasSuffix(fp, ".yml") ||
				strings.HasSuffix(fp, ".toml") || strings.HasSuffix(fp, ".txt") ||
				strings.HasSuffix(fp, ".sh") || strings.HasSuffix(fp, ".html") ||
				strings.HasSuffix(fp, ".css") {
				data, err := os.ReadFile(fp)
				if err != nil {
					return nil
				}
				relativePath, _ := filepath.Rel(s.root, fp)
				matches := s.grepLines(string(data), re, caseInsensitive, relativePath)
				results = append(results, matches...)
				matchCount += len(matches)
				if matchCount >= maxResults {
					return filepath.SkipDir
				}
			}
			return nil
		})
		if err != nil {
			return "", fmt.Errorf("walking directory: %s", err.Error())
		}
	} else {
		data, err := os.ReadFile(resolved)
		if err != nil {
			return "", fmt.Errorf("reading file: %s", err.Error())
		}
		relativePath, _ := filepath.Rel(s.root, resolved)
		results = s.grepLines(string(data), re, caseInsensitive, relativePath)
		matchCount = len(results)
	}

	if len(results) >= maxResults {
		results = results[:maxResults]
	}

	output := fmt.Sprintf("grep: %d matches found for pattern %q\n\n", matchCount, pattern)
	for _, line := range results {
		output += line + "\n"
	}
	return output, nil
}

func (s *sysTools) grepLines(content string, re *regexp.Regexp, caseInsensitive bool, path string) []string {
	var results []string
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		text := line
		if caseInsensitive {
			text = strings.ToLower(text)
		}
		if re.MatchString(text) {
			results = append(results, fmt.Sprintf("%s:%d: %s", path, i+1, line))
		}
	}
	return results
}

func (s *sysTools) countLines(_ context.Context, args map[string]interface{}) (string, error) {
	path := argString(args, "path")
	if path == "" {
		return "", errors.New("path is required and must be a string")
	}
	resolved, err := s.resolve(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return fmt.Sprintf("Error: %s is a directory, not a file", path), nil
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return "", fmt.Errorf("reading file: %s", err.Error())
	}
	lines := 0
	words := 0
	byteCount := len(data)
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	scanner.Split(bufio.ScanLines)
	for scanner.Scan() {
		lines++
		words += len(strings.Fields(scanner.Text()))
	}
	return fmt.Sprintf("%d lines, %d words, %d bytes: %s", lines, words, byteCount, path), nil
}

func (s *sysTools) listProcesses(ctx context.Context, args map[string]interface{}) (string, error) {
	filter := argString(args, "filter")
	cmd := exec.CommandContext(ctx, "ps", "aux")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("running ps: %s", err.Error())
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) > 1 {
		lines = lines[1:]
	}
	if filter != "" {
		var filtered []string
		for _, line := range lines {
			if strings.Contains(line, filter) {
				filtered = append(filtered, line)
			}
		}
		lines = filtered
	}
	if len(lines) > 50 {
		lines = lines[:50]
	}
	result := fmt.Sprintf("Process list (%d processes):\n", len(lines))
	for _, line := range lines {
		result += line + "\n"
	}
	return result, nil
}

func (s *sysTools) checkDiskSpace(ctx context.Context, args map[string]interface{}) (string, error) {
	path := "/"
	if p := argString(args, "path"); p != "" {
		path = p
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}
	cmd := exec.CommandContext(ctx, "df", "-h", absPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("running df: %s", err.Error())
	}
	return fmt.Sprintf("Disk usage for %s:\n%s", path, strings.TrimSpace(string(output))), nil
}

func (s *sysTools) fileInfo(_ context.Context, args map[string]interface{}) (string, error) {
	path := argString(args, "path")
	if path == "" {
		return "", errors.New("path is required and must be a string")
	}
	resolved, err := s.resolve(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	mode := info.Mode()
	perms := mode.Perm().String()
	size := info.Size()
	var sizeStr string
	switch {
	case size > 1024*1024*1024:
		sizeStr = fmt.Sprintf("%.1f GB", float64(size)/(1024*1024*1024))
	case size > 1024*1024:
		sizeStr = fmt.Sprintf("%.1f MB", float64(size)/(1024*1024))
	case size > 1024:
		sizeStr = fmt.Sprintf("%.1f KB", float64(size)/1024)
	default:
		sizeStr = fmt.Sprintf("%d bytes", size)
	}
	uid := info.Sys().(*syscall.Stat_t).Uid
	gid := info.Sys().(*syscall.Stat_t).Gid
	result := fmt.Sprintf("File: %s\n", path)
	result += fmt.Sprintf("  Size: %s\n", sizeStr)
	result += fmt.Sprintf("  Type: %s\n", map[bool]string{true: "directory", false: "file"}[info.IsDir()])
	result += fmt.Sprintf("  Permissions: %s (0%o)\n", perms, mode.Perm())
	result += fmt.Sprintf("  Owner UID: %d, Group GID: %d\n", uid, gid)
	result += fmt.Sprintf("  Modified: %s\n", info.ModTime().Format("2006-01-02 15:04:05"))
	return result, nil
}

func (s *sysTools) listEnvVars(_ context.Context, args map[string]interface{}) (string, error) {
	prefix := argString(args, "prefix")
	envVars := os.Environ()
	if prefix != "" {
		var filtered []string
		for _, env := range envVars {
			if strings.HasPrefix(env, prefix) {
				filtered = append(filtered, env)
			}
		}
		envVars = filtered
	}
	if len(envVars) > 100 {
		envVars = envVars[:100]
	}
	result := fmt.Sprintf("Environment variables (%d total):\n", len(envVars))
	for _, env := range envVars {
		result += env + "\n"
	}
	return result, nil
}

func (s *sysTools) findFiles(_ context.Context, args map[string]interface{}) (string, error) {
	pattern := argString(args, "pattern")
	if pattern == "" {
		return "", errors.New("'pattern' is required and must be a glob pattern (e.g., '*.go', 'memory/*.md', '**/*.txt')")
	}
	searchDir := s.root
	if dir := argString(args, "directory"); dir != "" {
		resolved, err := s.resolve(dir)
		if err != nil {
			return "", err
		}
		searchDir = resolved
	}
	searchDirAbs, err := filepath.Abs(searchDir)
	if err != nil {
		return "", fmt.Errorf("invalid search directory: %s", err.Error())
	}
	info, err := os.Stat(searchDirAbs)
	if err != nil {
		return "", fmt.Errorf("search directory not found: %s", searchDirAbs)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", searchDirAbs)
	}

	fullPattern := filepath.Join(searchDirAbs, pattern)

	type matchResult struct {
		relativePath string
		size         int64
	}
	var matches []matchResult

	if strings.Contains(pattern, "**") {
		err := filepath.WalkDir(searchDirAbs, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			rel, err := filepath.Rel(searchDirAbs, path)
			if err != nil {
				return nil
			}
			if rel == "." {
				return nil
			}
			if matchGlob(pattern, rel) {
				rootRel, _ := filepath.Rel(s.root, path)
				fi, _ := d.Info()
				var size int64
				if fi != nil {
					size = fi.Size()
				}
				matches = append(matches, matchResult{relativePath: rootRel, size: size})
			}
			return nil
		})
		if err != nil {
			return "", fmt.Errorf("searching: %s", err.Error())
		}
	} else {
		found, err := filepath.Glob(fullPattern)
		if err != nil {
			return "", fmt.Errorf("searching: %s", err.Error())
		}
		for _, f := range found {
			rootRel, _ := filepath.Rel(s.root, f)
			if rootRel == "." || strings.HasPrefix(rootRel, "..") {
				rootRel = f
			}
			fi, _ := os.Stat(f)
			var size int64
			if fi != nil {
				size = fi.Size()
			}
			matches = append(matches, matchResult{relativePath: rootRel, size: size})
		}
		if len(matches) == 0 && !strings.Contains(pattern, "**") {
			recPattern := "**/" + pattern
			err := filepath.WalkDir(searchDirAbs, func(path string, d os.DirEntry, err error) error {
				if err != nil {
					return nil
				}
				rel, err := filepath.Rel(searchDirAbs, path)
				if err != nil {
					return nil
				}
				if rel == "." {
					return nil
				}
				if matchGlob(recPattern, rel) {
					rootRel, _ := filepath.Rel(s.root, path)
					fi, _ := d.Info()
					var size int64
					if fi != nil {
						size = fi.Size()
					}
					matches = append(matches, matchResult{relativePath: rootRel, size: size})
				}
				return nil
			})
			if err != nil {
				return "", fmt.Errorf("searching: %s", err.Error())
			}
		}
	}

	if len(matches) == 0 {
		return fmt.Sprintf("No files found matching pattern '%s' in %s", pattern, searchDirAbs), nil
	}
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].relativePath < matches[j].relativePath
	})
	var lines []string
	for _, m := range matches {
		lines = append(lines, fmt.Sprintf("%s (%d bytes)", m.relativePath, m.size))
	}
	return fmt.Sprintf("Found %d file(s):\n%s", len(matches), strings.Join(lines, "\n")), nil
}

// matchGlob matches a glob pattern (possibly containing **) against a path.
// ** matches zero or more path segments; * matches within a single segment.
func matchGlob(pattern, path string) bool {
	return matchGlobParts(splitGlobPattern(pattern), 0, splitGlobPath(path), 0)
}

func splitGlobPath(p string) []string {
	parts := strings.Split(p, string(filepath.Separator))
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

func splitGlobPattern(p string) []string {
	parts := strings.Split(p, "/")
	var result []string
	for _, part := range parts {
		if part == "" {
			continue
		}
		if part == "**" {
			result = append(result, "**")
			continue
		}
		if clean := strings.TrimRight(part, "/"); clean != "" {
			result = append(result, clean)
		}
	}
	return result
}

func matchGlobParts(pparts []string, pi int, parts []string, si int) bool {
	if pi == len(pparts) {
		return si == len(parts)
	}
	if pparts[pi] == "**" {
		if pi == len(pparts)-1 {
			return true
		}
		for i := si; i <= len(parts); i++ {
			if matchGlobParts(pparts, pi+1, parts, i) {
				return true
			}
		}
		return false
	}
	if si >= len(parts) {
		return false
	}
	matched, err := filepath.Match(pparts[pi], parts[si])
	if err != nil || !matched {
		return false
	}
	return matchGlobParts(pparts, pi+1, parts, si+1)
}

// -----------------------------------------------------------------------------
// miscTools: date, system status, sleep, and model info.
// -----------------------------------------------------------------------------

// miscTools provides host/date/time utilities.
type miscTools struct{}

func (m *miscTools) register(r *Registry) {
	r.Register(&Tool{
		Name:        "date_now",
		Description: "Get the current date and time in the user's local timezone (US Eastern; EST or EDT depending on the date).",
		ReadOnly:    true,
		Handler:     m.dateNow,
	})
	r.Register(&Tool{
		Name:        "system_status",
		Description: "Current date/time plus system uptime and boot time (reads /proc/uptime on Linux).",
		ReadOnly:    true,
		Handler:     m.systemStatus,
	})
	// sleep and model_info intentionally live in goclaw, not here: sleep drives a
	// Telegram countdown, and model_info must follow goclaw's runtime /model
	// backend switches. Keeping single implementations avoids drift.
}

func localESTNow() time.Time {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		loc, _ = time.LoadLocation("EST5EDT")
	}
	return time.Now().In(loc)
}

func (m *miscTools) dateNow(_ context.Context, _ map[string]interface{}) (string, error) {
	local := localESTNow()
	return fmt.Sprintf("The current date and time is %s (%s)",
		local.Format("Monday, January 2, 2006"),
		local.Format("3:04 PM MST")), nil
}

func (m *miscTools) systemStatus(_ context.Context, _ map[string]interface{}) (string, error) {
	now := time.Now()
	local := localESTNow()
	result := fmt.Sprintf("Current date and time: %s (%s)\n",
		local.Format("Monday, January 2, 2006"),
		local.Format("3:04 PM MST"))

	uptimeSecs, bootTime, err := readSystemUptime()
	if err != nil {
		result += fmt.Sprintf("System uptime: unavailable (%s)", err.Error())
	} else {
		result += fmt.Sprintf("System uptime: %s\n", humanizeTime(now.Add(-time.Duration(uptimeSecs)*time.Second)))
		result += fmt.Sprintf("System booted: %s\n", humanizeTime(bootTime))
	}
	return result, nil
}

// readSystemUptime reads /proc/uptime, returning uptime seconds and boot time.
func readSystemUptime() (float64, time.Time, error) {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("cannot read /proc/uptime: %w", err)
	}
	re := regexp.MustCompile(`^(\d+\.\d+)`)
	matches := re.FindSubmatch(data)
	if len(matches) < 2 {
		return 0, time.Time{}, fmt.Errorf("unexpected /proc/uptime format")
	}
	secs, err := strconv.ParseFloat(string(matches[1]), 64)
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("cannot parse uptime: %w", err)
	}
	return secs, time.Now().Add(-time.Duration(secs) * time.Second), nil
}

// humanizeTime renders a relative time like "3 hours ago" / "in 2 days".
func humanizeTime(t time.Time) string {
	d := time.Since(t)
	suffix := "ago"
	if d < 0 {
		d = -d
		suffix = "from now"
	}
	unit := func(n int, name string) string {
		if n == 1 {
			return fmt.Sprintf("1 %s %s", name, suffix)
		}
		return fmt.Sprintf("%d %ss %s", n, name, suffix)
	}
	switch {
	case d < time.Second:
		return "now"
	case d < time.Minute:
		return unit(int(d.Seconds()), "second")
	case d < time.Hour:
		return unit(int(d.Minutes()), "minute")
	case d < 24*time.Hour:
		return unit(int(d.Hours()), "hour")
	case d < 30*24*time.Hour:
		return unit(int(d.Hours()/24), "day")
	case d < 365*24*time.Hour:
		return unit(int(d.Hours()/(24*30)), "month")
	default:
		return unit(int(d.Hours()/(24*365)), "year")
	}
}

// -----------------------------------------------------------------------------
// memoryTools: search/list of the workspace memory files.
// -----------------------------------------------------------------------------

// memoryTools is the agent's persistent memory: notes that survive restarts.
// Storage is namespaced per authenticated caller — each bearer-token principal
// gets its own <base>/<principal>/ directory (MEMORY.md + memory/*.md), so
// different agents wired to the same server never see each other's memory.
type memoryTools struct{ base string }

func (mt *memoryTools) register(r *Registry) {
	r.Register(&Tool{
		Name:        "memory_save",
		Description: "Persist a durable note to your long-term memory (survives restarts). Save facts, decisions, and context worth recalling later. Retrieve with memory_search / memory_read.",
		Schema: obj(map[string]interface{}{
			"text":  strProp("The note to remember"),
			"title": strProp("Optional short title/topic for the note"),
		}, "text"),
		Handler: mt.save,
	})
	r.Register(&Tool{
		Name:        "memory_search",
		Description: "Search your long-term memory (MEMORY.md and memory/*.md) for a keyword and return matching lines. Use before answering questions about prior context.",
		Schema:      obj(map[string]interface{}{"query": strProp("Keyword or phrase to search for")}, "query"),
		ReadOnly:    true,
		Handler:     mt.search,
	})
	r.Register(&Tool{
		Name:        "memory_read",
		Description: "Read a memory file in full. Defaults to MEMORY.md (the main index); pass a name from memory_list to read a specific one.",
		Schema:      obj(map[string]interface{}{"file": strProp("Memory file to read, relative to your memory dir (default MEMORY.md)")}),
		ReadOnly:    true,
		Handler:     mt.read,
	})
	r.Register(&Tool{
		Name:        "memory_list",
		Description: "List your memory files (MEMORY.md and memory/*.md) with their sizes.",
		ReadOnly:    true,
		Handler:     mt.list,
	})
}

// memoryPrincipal returns a filesystem-safe namespace derived from the
// authenticated caller. Callers with no principal share the "shared" namespace.
func memoryPrincipal(ctx context.Context) string {
	p := filepath.Base(strings.TrimSpace(mcp.PrincipalFrom(ctx)))
	if p == "" || p == "." || p == "-" || p == string(filepath.Separator) {
		return "shared"
	}
	var b strings.Builder
	for _, r := range p {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	if b.Len() == 0 {
		return "shared"
	}
	return b.String()
}

// dir returns this caller's namespaced memory directory.
func (mt *memoryTools) dir(ctx context.Context) (string, error) {
	if mt.base == "" {
		return "", errors.New("no memory directory configured on this server")
	}
	return filepath.Join(mt.base, memoryPrincipal(ctx)), nil
}

func (mt *memoryTools) save(ctx context.Context, args map[string]interface{}) (string, error) {
	text := strings.TrimSpace(argString(args, "text"))
	if text == "" {
		return "", errors.New("text is required and must be a non-empty string")
	}
	dir, err := mt.dir(ctx)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	title := strings.TrimSpace(argString(args, "title"))
	var entry strings.Builder
	entry.WriteString("\n\n## ")
	entry.WriteString(time.Now().Format(time.RFC3339))
	if title != "" {
		entry.WriteString(" — ")
		entry.WriteString(title)
	}
	entry.WriteString("\n")
	entry.WriteString(text)
	entry.WriteString("\n")
	f, err := os.OpenFile(filepath.Join(dir, "MEMORY.md"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.WriteString(entry.String()); err != nil {
		return "", err
	}
	return fmt.Sprintf("Saved to memory (%s/MEMORY.md).", memoryPrincipal(ctx)), nil
}

func (mt *memoryTools) read(ctx context.Context, args map[string]interface{}) (string, error) {
	dir, err := mt.dir(ctx)
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(argString(args, "file"))
	if name == "" {
		name = "MEMORY.md"
	}
	clean := filepath.Clean(name)
	// Be forgiving about the memory/ subdir: try the name as given, then inside
	// the memory/ subfolder, so a bare "2026-07-17.md" from memory_list resolves.
	tries := []string{clean}
	sep := string(filepath.Separator)
	if !strings.HasPrefix(clean, "memory"+sep) && clean != "memory" {
		tries = append(tries, filepath.Join("memory", clean))
	}
	for _, t := range tries {
		full := filepath.Join(dir, t)
		if rel, err := filepath.Rel(dir, full); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+sep) {
			continue // escapes the memory dir
		}
		if data, err := os.ReadFile(full); err == nil {
			return string(data), nil
		}
	}
	return "", fmt.Errorf("memory file %q not found (looked in the memory dir and its memory/ subfolder — use memory_list to see exact names)", name)
}

func (mt *memoryTools) search(ctx context.Context, args map[string]interface{}) (string, error) {
	query := argString(args, "query")
	if query == "" {
		return "", errors.New("query is required and must be a non-empty string")
	}
	lowerQuery := strings.ToLower(query)
	workspace, err := mt.dir(ctx)
	if err != nil {
		return "", err
	}
	memoryDir := filepath.Join(workspace, "memory")

	files := make(map[string]bool)
	rootMem := filepath.Join(workspace, "MEMORY.md")
	if _, err := os.Stat(rootMem); err == nil {
		files[rootMem] = true
	}
	if _, err := os.Stat(memoryDir); err == nil {
		if entries, err := os.ReadDir(memoryDir); err == nil {
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
					files[filepath.Join(memoryDir, e.Name())] = true
				}
			}
		}
	}
	if len(files) == 0 {
		return "No memory files found in workspace.", nil
	}

	type match struct {
		file    string
		lineNum int
		line    string
	}
	var allMatches []match

	filePaths := make([]string, 0, len(files))
	for f := range files {
		filePaths = append(filePaths, f)
	}
	sort.Strings(filePaths)

	for _, filePath := range filePaths {
		data, err := os.ReadFile(filePath)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(strings.NewReader(string(data)))
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			line := scanner.Text()
			if strings.Contains(strings.ToLower(line), lowerQuery) {
				allMatches = append(allMatches, match{file: filePath, lineNum: lineNum, line: line})
			}
		}
	}

	if len(allMatches) == 0 {
		return fmt.Sprintf("No matches found for query: %q", query), nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("=== Memory Search Results (%d matches) ===\n", len(allMatches)))
	sb.WriteString(fmt.Sprintf("Subject: %s\n\n", query))

	currentFile := ""
	for _, mm := range allMatches {
		displayPath := mm.file
		if rel, err := filepath.Rel(workspace, mm.file); err == nil {
			displayPath = rel
		}
		if displayPath != currentFile {
			if currentFile != "" {
				sb.WriteString("\n")
			}
			currentFile = displayPath
			sb.WriteString(fmt.Sprintf("--- %s ---\n", displayPath))
		}
		sb.WriteString(fmt.Sprintf("  L%d: %s\n", mm.lineNum, mm.line))
	}
	return sb.String(), nil
}

// memEntry is one memory file in the table-of-contents listing.
type memEntry struct {
	arg     string // exact value to pass to memory_read
	size    int64
	mod     time.Time
	preview string // first meaningful line
}

// memPreview returns the first non-empty line of a file, stripped of markdown
// heading/list markers and truncated — a one-line "what's in here" hint.
func memPreview(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		line = strings.TrimLeft(line, "#>-*ﾠ ")
		if line == "" {
			continue
		}
		r := []rune(line)
		if len(r) > 90 {
			line = string(r[:89]) + "…"
		}
		return line
	}
	return ""
}

func (mt *memoryTools) list(ctx context.Context, _ map[string]interface{}) (string, error) {
	workspace, err := mt.dir(ctx)
	if err != nil {
		return "", err
	}

	var entries []memEntry
	if info, err := os.Stat(filepath.Join(workspace, "MEMORY.md")); err == nil {
		entries = append(entries, memEntry{"MEMORY.md", info.Size(), info.ModTime(), memPreview(filepath.Join(workspace, "MEMORY.md"))})
	}
	memoryDir := filepath.Join(workspace, "memory")
	if des, err := os.ReadDir(memoryDir); err == nil {
		for _, e := range des {
			if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
				continue
			}
			full := filepath.Join(memoryDir, e.Name())
			info, err := os.Stat(full)
			if err != nil {
				continue
			}
			entries = append(entries, memEntry{"memory/" + e.Name(), info.Size(), info.ModTime(), memPreview(full)})
		}
	}
	if len(entries) == 0 {
		return "No memory yet. Use memory_save to record something, or memory_search to look for a topic.", nil
	}

	// Freshest first, so "what happened recently" is answerable at a glance.
	sort.Slice(entries, func(i, j int) bool { return entries[i].mod.After(entries[j].mod) })

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Your memory — %d file(s), newest first. Pass the name to memory_read; use memory_search to find a topic across all of them.\n\n", len(entries)))
	for _, e := range entries {
		sb.WriteString(fmt.Sprintf("%-26s  %s  %4.1f KB\n", e.arg, e.mod.Format("2006-01-02"), float64(e.size)/1024))
		if e.preview != "" {
			sb.WriteString("    " + e.preview + "\n")
		}
	}
	return sb.String(), nil
}
