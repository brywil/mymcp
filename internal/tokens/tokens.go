// Package tokens implements a directory-backed set of named bearer tokens —
// one file per token, so tokens are hand-manageable (drop a file in, delete to
// revoke) and each carries a name used for per-request logging.
//
//	<dir>/<name>   # file, mode 0600, contents = the secret token
package tokens

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Store is a directory of named bearer tokens.
type Store struct{ dir string }

// Load opens (creating if needed) the token directory.
func Load(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

// Dir returns the backing directory.
func (s *Store) Dir() string { return s.dir }

var nameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func (s *Store) path(name string) (string, error) {
	if name == "" || name == "." || name == ".." || !nameRe.MatchString(name) {
		return "", fmt.Errorf("invalid token name %q (use letters, digits, . _ -)", name)
	}
	return filepath.Join(s.dir, name), nil
}

// List returns the token names, sorted.
func (s *Store) List() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// Count returns the number of tokens.
func (s *Store) Count() int {
	n, _ := s.List()
	return len(n)
}

// Add creates a new random token under name, erroring if it already exists.
func (s *Store) Add(name string) (string, error) {
	p, err := s.path(name)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(p); err == nil {
		return "", fmt.Errorf("token %q already exists", name)
	}
	secret, err := gen()
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(p, []byte(secret+"\n"), 0o600); err != nil {
		return "", err
	}
	return secret, nil
}

// Get returns the secret for name.
func (s *Store) Get(name string) (string, error) {
	p, err := s.path(name)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// GetOrCreate returns name's secret, creating it if absent.
func (s *Store) GetOrCreate(name string) (string, error) {
	if t, err := s.Get(name); err == nil && t != "" {
		return t, nil
	}
	return s.Add(name)
}

// Revoke deletes a token.
func (s *Store) Revoke(name string) error {
	p, err := s.path(name)
	if err != nil {
		return err
	}
	if _, err := os.Stat(p); err != nil {
		return fmt.Errorf("no token named %q", name)
	}
	return os.Remove(p)
}

// EnsureDefault creates a "default" token if the store is empty. Reports whether
// it created one.
func (s *Store) EnsureDefault() (bool, error) {
	if s.Count() > 0 {
		return false, nil
	}
	_, err := s.Add("default")
	return err == nil, err
}

// Authenticate returns the name of the token matching presented (constant-time),
// and whether a match was found. The directory is re-read each call, so adding
// or revoking a token takes effect immediately.
func (s *Store) Authenticate(presented string) (string, bool) {
	if presented == "" {
		return "", false
	}
	names, err := s.List()
	if err != nil {
		return "", false
	}
	matched := ""
	for _, name := range names {
		secret, err := s.Get(name)
		if err != nil {
			continue
		}
		// Compare all (no early return) so timing does not reveal position.
		if subtle.ConstantTimeCompare([]byte(presented), []byte(secret)) == 1 {
			matched = name
		}
	}
	return matched, matched != ""
}

func gen() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
