// Package pubsub is topic-based messaging between agents.
//
// The point is collaboration: one agent publishes a question or a result, others
// pick it up on their own schedule. Nothing here blocks — a subscriber reads when
// it next takes a turn, which is what keeps this from interfering with an agent's
// tool-calling loop.
//
// Two properties make this pub/sub rather than a queue:
//
//   - Reads are NON-destructive. Each subscriber has its own cursor per topic, so
//     three agents subscribed to "builds" each see every message. A queue would let
//     whoever polled first consume it.
//   - Identity comes from the caller's authenticated principal, never from an
//     argument. An agent cannot publish as someone else without their bearer token.
//
// Direct messages need no separate mechanism: a DM is the topic "dm.<principal>".
//
// Storage is behind Store so the backing medium stays swappable — FileStore works
// for a single host and, on a shared mount, for several hosts with no network path
// between them (which matters when connections are only permitted in one
// direction). Nothing in the tool contract exposes a path.
package pubsub

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Message is one published item.
type Message struct {
	ID    string    `json:"id"` // sortable: <unixnano>-<seq>, so lexical order is chronological
	Topic string    `json:"topic"`
	From  string    `json:"from"` // publisher's principal, set by the server not the caller
	Body  string    `json:"body"`
	Time  time.Time `json:"time"`
}

// Agent is a principal that has been seen recently.
type Agent struct {
	Principal string    `json:"principal"`
	LastSeen  time.Time `json:"last_seen"`
	Topics    []string  `json:"topics"` // what it subscribes to
}

// Store is the backing medium. Implementations must be safe for concurrent use
// by multiple processes, not just goroutines — several agents on one host talk to
// one server, and on a shared mount several hosts talk to the same files.
type Store interface {
	Publish(topic, from, body string) (Message, error)
	Subscribe(principal, topic string) error
	Unsubscribe(principal, topic string) error
	Subscriptions(principal string) ([]string, error)
	// Poll returns messages after this principal's cursor and advances it. With no
	// topics, it polls everything the principal subscribes to.
	Poll(principal string, topics []string, max int) ([]Message, error)
	// Peek is Poll without advancing, for inspection.
	Peek(principal string, topics []string, max int) ([]Message, error)
	Agents(within time.Duration) ([]Agent, error)
	Touch(principal string) error
}

// Retention bounds. Messages are not kept forever: a topic nobody drains would
// otherwise grow without limit, and a cursor pointing at a deleted message must
// still behave (it does — cursors compare by ID, and a missing message is simply
// never returned).
const (
	MaxAge      = 7 * 24 * time.Hour
	MaxPerTopic = 500
	MaxBody     = 256 * 1024
)

// ErrNoSuchTopic is returned when polling a topic that has never existed, which
// is nearly always a typo. Silence here is what makes messaging untrustworthy.
var ErrNoSuchTopic = errors.New("no such topic")

// FileStore keeps everything under one directory.
//
//	<base>/topics/<topic>/<msgid>.json
//	<base>/subs/<principal>/<topic>       (empty marker file)
//	<base>/cursors/<principal>/<topic>    (contains the last-read message ID)
//	<base>/presence/<principal>           (mtime is the last-seen time)
//
// Every write is temp-then-rename, which is atomic on POSIX and on NFS within a
// directory, so a reader never observes a half-written message and no locking is
// needed: one writer per file.
type FileStore struct {
	base string
	mu   sync.Mutex // serialises ID generation within this process
	seq  int
	last int64
}

// NewFileStore returns a store rooted at base, creating it if needed.
func NewFileStore(base string) (*FileStore, error) {
	if strings.TrimSpace(base) == "" {
		return nil, errors.New("pubsub: no storage directory configured")
	}
	for _, d := range []string{"topics", "subs", "cursors", "presence"} {
		if err := os.MkdirAll(filepath.Join(base, d), 0o755); err != nil {
			return nil, fmt.Errorf("pubsub: %w", err)
		}
	}
	return &FileStore{base: base}, nil
}

// safeName makes an arbitrary topic or principal usable as ONE path element.
// Separators are folded rather than preserved: a topic is a name, not a path, and
// letting "../.." through would let a caller read or write outside the store.
func safeName(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + 32) // topics are case-insensitive; "Builds" and "builds" are one topic
		default:
			b.WriteRune('_')
		}
	}
	out := strings.Trim(b.String(), ".")
	if out == "" || out == "." || out == ".." {
		return ""
	}
	if len(out) > 128 {
		out = out[:128]
	}
	return out
}

// ValidTopic reports whether a topic name survives sanitising, and returns it.
func ValidTopic(topic string) (string, bool) {
	t := safeName(topic)
	return t, t != ""
}

func (f *FileStore) topicDir(t string) string     { return filepath.Join(f.base, "topics", t) }
func (f *FileStore) subDir(p string) string       { return filepath.Join(f.base, "subs", p) }
func (f *FileStore) cursorDir(p string) string    { return filepath.Join(f.base, "cursors", p) }
func (f *FileStore) presenceFile(p string) string { return filepath.Join(f.base, "presence", p) }

// writeAtomic writes via a temp file in the SAME directory then renames. Same
// directory matters: rename is only atomic within a filesystem, and on NFS only
// within a directory.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // no-op once renamed
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// nextID returns a lexically sortable, collision-free ID. The seq counter breaks
// ties when two publishes land in the same nanosecond, and the width is fixed so
// string ordering matches time ordering.
func (f *FileStore) nextID(now time.Time) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	ns := now.UnixNano()
	if ns == f.last {
		f.seq++
	} else {
		f.last, f.seq = ns, 0
	}
	return fmt.Sprintf("%019d-%04d", ns, f.seq)
}

// Publish appends a message to a topic.
func (f *FileStore) Publish(topic, from, body string) (Message, error) {
	t, ok := ValidTopic(topic)
	if !ok {
		return Message{}, fmt.Errorf("invalid topic %q", topic)
	}
	if len(body) > MaxBody {
		return Message{}, fmt.Errorf("message too large: %d bytes (max %d)", len(body), MaxBody)
	}
	now := time.Now()
	m := Message{ID: f.nextID(now), Topic: t, From: safeName(from), Body: body, Time: now}
	data, err := json.Marshal(m)
	if err != nil {
		return Message{}, err
	}
	if err := writeAtomic(filepath.Join(f.topicDir(t), m.ID+".json"), data); err != nil {
		return Message{}, err
	}
	f.gc(t)
	return m, nil
}

// gc trims a topic to the retention bounds. Best-effort: failures here must never
// fail a publish.
func (f *FileStore) gc(topic string) {
	ents, err := os.ReadDir(f.topicDir(topic))
	if err != nil {
		return
	}
	var names []string
	for _, e := range ents {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names) // chronological, by ID construction
	cutoff := time.Now().Add(-MaxAge)
	drop := 0
	if len(names) > MaxPerTopic {
		drop = len(names) - MaxPerTopic
	}
	for i, n := range names {
		if i < drop {
			os.Remove(filepath.Join(f.topicDir(topic), n))
			continue
		}
		fi, err := os.Stat(filepath.Join(f.topicDir(topic), n))
		if err == nil && fi.ModTime().Before(cutoff) {
			os.Remove(filepath.Join(f.topicDir(topic), n))
		}
	}
}

// Subscribe records interest. Idempotent.
func (f *FileStore) Subscribe(principal, topic string) error {
	p, t := safeName(principal), safeName(topic)
	if p == "" || t == "" {
		return fmt.Errorf("invalid principal or topic")
	}
	if err := os.MkdirAll(f.topicDir(t), 0o755); err != nil { // creating a topic is implicit
		return err
	}
	return writeAtomic(filepath.Join(f.subDir(p), t), nil)
}

// Unsubscribe removes interest and the cursor. Idempotent.
func (f *FileStore) Unsubscribe(principal, topic string) error {
	p, t := safeName(principal), safeName(topic)
	if p == "" || t == "" {
		return fmt.Errorf("invalid principal or topic")
	}
	os.Remove(filepath.Join(f.subDir(p), t))
	os.Remove(filepath.Join(f.cursorDir(p), t))
	return nil
}

// Subscriptions lists a principal's topics.
func (f *FileStore) Subscriptions(principal string) ([]string, error) {
	p := safeName(principal)
	ents, err := os.ReadDir(f.subDir(p))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if !e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

func (f *FileStore) readCursor(p, t string) string {
	b, err := os.ReadFile(filepath.Join(f.cursorDir(p), t))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// read gathers messages after the cursor for one topic.
func (f *FileStore) read(p, t string, advance bool, max int) ([]Message, error) {
	dir := f.topicDir(t)
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNoSuchTopic
		}
		return nil, err
	}
	cur := f.readCursor(p, t)
	var names []string
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".json") || strings.HasPrefix(n, ".") {
			continue
		}
		if id := strings.TrimSuffix(n, ".json"); id > cur {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	if max > 0 && len(names) > max {
		names = names[:max]
	}
	var out []Message
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			continue // raced with gc; skipping is correct
		}
		var m Message
		if json.Unmarshal(b, &m) == nil {
			out = append(out, m)
		}
	}
	if advance && len(out) > 0 {
		last := out[len(out)-1].ID
		_ = writeAtomic(filepath.Join(f.cursorDir(p), t), []byte(last))
	}
	return out, nil
}

func (f *FileStore) collect(principal string, topics []string, max int, advance bool) ([]Message, error) {
	p := safeName(principal)
	if p == "" {
		return nil, errors.New("no principal: this server cannot tell agents apart, so messaging is disabled")
	}
	if len(topics) == 0 {
		subs, err := f.Subscriptions(p)
		if err != nil {
			return nil, err
		}
		topics = subs
	}
	var all []Message
	var missing []string
	for _, raw := range topics {
		t, ok := ValidTopic(raw)
		if !ok {
			continue
		}
		ms, err := f.read(p, t, advance, max)
		if errors.Is(err, ErrNoSuchTopic) {
			missing = append(missing, raw)
			continue
		}
		if err != nil {
			return nil, err
		}
		all = append(all, ms...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	if max > 0 && len(all) > max {
		all = all[:max]
	}
	if len(missing) > 0 && len(all) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrNoSuchTopic, strings.Join(missing, ", "))
	}
	return all, nil
}

// Poll returns new messages and advances the cursor.
func (f *FileStore) Poll(principal string, topics []string, max int) ([]Message, error) {
	return f.collect(principal, topics, max, true)
}

// Peek returns new messages without advancing.
func (f *FileStore) Peek(principal string, topics []string, max int) ([]Message, error) {
	return f.collect(principal, topics, max, false)
}

// Touch records that a principal is alive.
func (f *FileStore) Touch(principal string) error {
	p := safeName(principal)
	if p == "" {
		return nil
	}
	return writeAtomic(f.presenceFile(p), []byte(time.Now().UTC().Format(time.RFC3339)))
}

// Agents lists principals seen within the window. Presence is observed, not
// self-declared: an agent appears because it actually called something.
func (f *FileStore) Agents(within time.Duration) ([]Agent, error) {
	ents, err := os.ReadDir(filepath.Join(f.base, "presence"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	cutoff := time.Now().Add(-within)
	var out []Agent
	for _, e := range ents {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		if within > 0 && fi.ModTime().Before(cutoff) {
			continue
		}
		subs, _ := f.Subscriptions(e.Name())
		out = append(out, Agent{Principal: e.Name(), LastSeen: fi.ModTime(), Topics: subs})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Principal < out[j].Principal })
	return out, nil
}

// KnownAgents lists every principal ever seen, regardless of recency — used to
// tell "you addressed a typo" apart from "that agent is idle", which are very
// different problems for the caller.
func (f *FileStore) KnownAgents() ([]string, error) {
	ags, err := f.Agents(0)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ags))
	for _, a := range ags {
		out = append(out, a.Principal)
	}
	return out, nil
}
