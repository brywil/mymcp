// Package policy implements per-principal authorization keyed on the client
// certificate CN. Each principal is a hand-editable file in a directory:
//
//	<dir>/<slug>.principal
//	    cn    = Alice Example
//	    allow = read_file, list_directory, ro
//
// Grant tokens are tool names, or the presets "ro" (all read-only tools) and
// "all" (everything). Authorization is deny-by-default. See DESIGN.md.
package policy

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Enforcer authorizes tool calls per principal.
type Enforcer struct{ dir string }

// Load builds an Enforcer over dir, creating it if absent.
func Load(dir string) (*Enforcer, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Enforcer{dir: dir}, nil
}

// Principal is a loaded principal record.
type Principal struct {
	CN    string
	Allow []string // grant tokens (tool names, "ro", "all")
	Path  string
}

func (e *Enforcer) load() map[string]Principal {
	out := map[string]Principal{}
	entries, err := os.ReadDir(e.dir)
	if err != nil {
		return out
	}
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".principal") {
			continue
		}
		p := filepath.Join(e.dir, ent.Name())
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		pr := Principal{Path: p}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			key, val, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			key, val = strings.TrimSpace(key), strings.TrimSpace(val)
			switch key {
			case "cn":
				pr.CN = val
			case "allow":
				for _, tok := range strings.Split(val, ",") {
					if tok = strings.TrimSpace(tok); tok != "" {
						pr.Allow = append(pr.Allow, tok)
					}
				}
			}
		}
		if pr.CN != "" {
			out[pr.CN] = pr
		}
	}
	return out
}

// Known reports whether cn has a registered principal.
func (e *Enforcer) Known(cn string) bool {
	_, ok := e.load()[cn]
	return ok
}

// Allow reports whether cn may call tool (readOnly indicates the tool's class).
func (e *Enforcer) Allow(cn, tool string, readOnly bool) bool {
	pr, ok := e.load()[cn]
	if !ok {
		return false
	}
	for _, g := range pr.Allow {
		switch g {
		case "all", "*":
			return true
		case "ro":
			if readOnly {
				return true
			}
		default:
			if g == tool {
				return true
			}
		}
	}
	return false
}

// List returns all principals sorted by CN.
func (e *Enforcer) List() []Principal {
	m := e.load()
	out := make([]Principal, 0, len(m))
	for _, p := range m {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CN < out[j].CN })
	return out
}

var slugRe = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func (e *Enforcer) pathFor(cn string) (string, bool) {
	// reuse an existing file if this CN already has one
	for _, p := range e.load() {
		if p.CN == cn {
			return p.Path, true
		}
	}
	slug := strings.Trim(slugRe.ReplaceAllString(cn, "-"), "-")
	if slug == "" {
		slug = "principal"
	}
	return filepath.Join(e.dir, slug+".principal"), false
}

// Add registers a principal (no grants) if it does not already exist.
func (e *Enforcer) Add(cn string) error {
	path, exists := e.pathFor(cn)
	if exists {
		return nil
	}
	return e.write(path, Principal{CN: cn})
}

// Grant adds a capability token ("ro", "all", or a tool name) to cn, creating
// the principal if needed. Returns the resulting grant list.
func (e *Enforcer) Grant(cn, cap string) ([]string, error) {
	path, _ := e.pathFor(cn)
	pr := e.load()[cn]
	pr.CN = cn
	for _, g := range pr.Allow {
		if g == cap {
			return pr.Allow, nil // already granted
		}
	}
	pr.Allow = append(pr.Allow, cap)
	sort.Strings(pr.Allow)
	if err := e.write(path, pr); err != nil {
		return nil, err
	}
	return pr.Allow, nil
}

// Remove deletes a principal.
func (e *Enforcer) Remove(cn string) error {
	if p, ok := e.load()[cn]; ok {
		return os.Remove(p.Path)
	}
	return fmt.Errorf("no principal with CN %q", cn)
}

func (e *Enforcer) write(path string, pr Principal) error {
	var b strings.Builder
	fmt.Fprintf(&b, "cn = %s\n", pr.CN)
	fmt.Fprintf(&b, "allow = %s\n", strings.Join(pr.Allow, ", "))
	return os.WriteFile(path, []byte(b.String()), 0o600)
}
