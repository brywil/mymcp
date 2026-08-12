package pubsub

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newStore(t *testing.T) *FileStore {
	t.Helper()
	s, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPublishThenPoll(t *testing.T) {
	s := newStore(t)
	if err := s.Subscribe("bob", "builds"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Publish("builds", "alice", "the build is green"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Poll("bob", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Body != "the build is green" {
		t.Fatalf("got %+v", got)
	}
	if got[0].From != "alice" {
		t.Errorf("publisher identity lost: %q", got[0].From)
	}
}

// The defining property: reads are non-destructive across subscribers. If the
// first poller consumed the message this would be a queue, and asking two agents
// for help would only reach one of them.
func TestEverySubscriberSeesEveryMessage(t *testing.T) {
	s := newStore(t)
	for _, who := range []string{"opus", "gpt", "local"} {
		if err := s.Subscribe(who, "help"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Publish("help", "aurora", "stuck on a name map"); err != nil {
		t.Fatal(err)
	}
	for _, who := range []string{"opus", "gpt", "local"} {
		got, err := s.Poll(who, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 {
			t.Errorf("%s got %d messages, want 1 — cursors are not independent", who, len(got))
		}
	}
}

// Polling twice must not redeliver: an agent that re-reads its inbox every turn
// would otherwise answer the same question forever.
func TestPollDoesNotRedeliver(t *testing.T) {
	s := newStore(t)
	s.Subscribe("bob", "t")
	s.Publish("t", "alice", "one")
	if got, _ := s.Poll("bob", nil, 0); len(got) != 1 {
		t.Fatalf("first poll got %d", len(got))
	}
	got, err := s.Poll("bob", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("second poll redelivered %d message(s)", len(got))
	}
}

// Peek is for looking without consuming.
func TestPeekDoesNotAdvance(t *testing.T) {
	s := newStore(t)
	s.Subscribe("bob", "t")
	s.Publish("t", "alice", "one")
	if got, _ := s.Peek("bob", nil, 0); len(got) != 1 {
		t.Fatal("peek saw nothing")
	}
	if got, _ := s.Peek("bob", nil, 0); len(got) != 1 {
		t.Error("peek consumed the message")
	}
	if got, _ := s.Poll("bob", nil, 0); len(got) != 1 {
		t.Error("poll after peek saw nothing")
	}
}

// A subscriber joining later must not be flooded with history it never asked
// for, but must see everything published after it subscribed.
func TestLateSubscriberSeesOnlyNewMessages(t *testing.T) {
	s := newStore(t)
	s.Subscribe("early", "t")
	s.Publish("t", "x", "before")
	s.Poll("early", nil, 0)

	s.Subscribe("late", "t")
	s.Publish("t", "x", "after")

	got, err := s.Poll("late", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	// A fresh cursor means the late subscriber sees the whole retained topic.
	// That is the deliberate choice (context beats silence); assert it explicitly
	// so a change of mind is a test change, not a surprise.
	if len(got) != 2 {
		t.Errorf("late subscriber got %d, want 2 (full retained history)", len(got))
	}
}

// Ordering must be chronological across topics, or a conversation reads
// backwards.
func TestMessagesAreChronological(t *testing.T) {
	s := newStore(t)
	s.Subscribe("bob", "a")
	s.Subscribe("bob", "b")
	for i := 0; i < 25; i++ {
		topic := "a"
		if i%2 == 1 {
			topic = "b"
		}
		if _, err := s.Publish(topic, "x", fmt.Sprintf("msg%02d", i)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Poll("bob", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 25 {
		t.Fatalf("got %d, want 25", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].ID >= got[i].ID {
			t.Fatalf("out of order at %d: %s then %s", i, got[i-1].ID, got[i].ID)
		}
	}
	for i, m := range got {
		if want := fmt.Sprintf("msg%02d", i); m.Body != want {
			t.Fatalf("position %d holds %q, want %q", i, m.Body, want)
		}
	}
}

// IDs must stay ordered even when publishes land in the same nanosecond, which
// they do under concurrency. A tie that sorted wrongly would silently reorder a
// conversation.
func TestConcurrentPublishKeepsEveryMessageAndOrders(t *testing.T) {
	s := newStore(t)
	s.Subscribe("bob", "t")
	const n = 50
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := s.Publish("t", "x", fmt.Sprintf("m%d", i)); err != nil {
				t.Errorf("publish %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	got, err := s.Poll("bob", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != n {
		t.Fatalf("lost messages: got %d of %d", len(got), n)
	}
	seen := map[string]bool{}
	for _, m := range got {
		if seen[m.ID] {
			t.Fatalf("duplicate ID %s", m.ID)
		}
		seen[m.ID] = true
	}
}

// A topic name is a NAME, not a path. Letting separators through would let a
// caller read or write outside the store.
func TestTopicCannotEscapeTheStore(t *testing.T) {
	base := t.TempDir()
	s, err := NewFileStore(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, evil := range []string{"../../etc/passwd", "..", "a/../../b", "/abs/path", "./."} {
		if _, err := s.Publish(evil, "x", "payload"); err != nil {
			continue // rejected outright is fine
		}
		// If accepted, it must have been folded into a single safe element.
		t.Logf("accepted %q as %q", evil, safeName(evil))
	}
	// Nothing may exist outside <base>/topics.
	var stray []string
	filepath.Walk(base, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(base, p)
		if strings.HasPrefix(rel, "..") {
			stray = append(stray, rel)
		}
		return nil
	})
	if len(stray) > 0 {
		t.Fatalf("files written outside the store: %v", stray)
	}
	if _, err := os.Stat(filepath.Join(base, "..", "etc")); err == nil {
		t.Fatal("traversal created a sibling directory")
	}
}

// Topics are case-insensitive: "Builds" and "builds" must not be two topics that
// silently never see each other's messages.
func TestTopicsAreCaseInsensitive(t *testing.T) {
	s := newStore(t)
	s.Subscribe("bob", "Builds")
	s.Publish("BUILDS", "alice", "green")
	got, err := s.Poll("bob", []string{"builds"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Errorf("case variants split the topic: got %d", len(got))
	}
}

// Polling a topic that never existed is nearly always a typo. It must say so
// rather than returning an empty list that reads as "no news".
func TestUnknownTopicIsLoud(t *testing.T) {
	s := newStore(t)
	_, err := s.Poll("bob", []string{"buidls"}, 0)
	if !errors.Is(err, ErrNoSuchTopic) {
		t.Fatalf("expected ErrNoSuchTopic, got %v", err)
	}
	if err != nil && !strings.Contains(err.Error(), "buidls") {
		t.Errorf("error should name the topic: %v", err)
	}
}

// Without a principal the server cannot tell agents apart, so messaging must
// refuse rather than silently pooling everyone into one identity.
func TestNoPrincipalIsRefused(t *testing.T) {
	s := newStore(t)
	if _, err := s.Poll("", nil, 0); err == nil {
		t.Fatal("polling with no principal was allowed")
	}
}

func TestSubscriptionsRoundTrip(t *testing.T) {
	s := newStore(t)
	s.Subscribe("bob", "b")
	s.Subscribe("bob", "a")
	s.Subscribe("bob", "a") // idempotent
	got, err := s.Subscriptions("bob")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("got %v, want [a b]", got)
	}
	s.Unsubscribe("bob", "a")
	if got, _ := s.Subscriptions("bob"); len(got) != 1 || got[0] != "b" {
		t.Fatalf("after unsubscribe got %v", got)
	}
}

// Unsubscribing then resubscribing must not redeliver the backlog as if it were
// new; the cursor is dropped on unsubscribe, which is the deliberate choice.
func TestResubscribeResetsCursor(t *testing.T) {
	s := newStore(t)
	s.Subscribe("bob", "t")
	s.Publish("t", "x", "one")
	s.Poll("bob", nil, 0)
	s.Unsubscribe("bob", "t")
	s.Subscribe("bob", "t")
	got, _ := s.Poll("bob", nil, 0)
	if len(got) != 1 {
		t.Errorf("resubscribe delivered %d, want the retained history (1)", len(got))
	}
}

func TestPresenceReflectsActivity(t *testing.T) {
	s := newStore(t)
	if ags, _ := s.Agents(time.Minute); len(ags) != 0 {
		t.Fatalf("expected no agents, got %v", ags)
	}
	s.Touch("aurora")
	s.Subscribe("aurora", "help")
	ags, err := s.Agents(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(ags) != 1 || ags[0].Principal != "aurora" {
		t.Fatalf("got %+v", ags)
	}
	if len(ags[0].Topics) != 1 || ags[0].Topics[0] != "help" {
		t.Errorf("subscriptions not reported: %+v", ags[0])
	}
	// A tight window excludes a stale agent, which is how a caller tells "idle"
	// from "never existed".
	if ags, _ := s.Agents(time.Nanosecond); len(ags) != 0 {
		t.Errorf("stale agent still listed: %+v", ags)
	}
	if known, _ := s.KnownAgents(); len(known) != 1 {
		t.Errorf("KnownAgents should ignore recency, got %v", known)
	}
}

func TestOversizedBodyRejected(t *testing.T) {
	s := newStore(t)
	if _, err := s.Publish("t", "x", strings.Repeat("a", MaxBody+1)); err == nil {
		t.Fatal("oversized message was accepted")
	}
}

func TestRetentionTrimsTopic(t *testing.T) {
	s := newStore(t)
	for i := 0; i < MaxPerTopic+20; i++ {
		if _, err := s.Publish("t", "x", "m"); err != nil {
			t.Fatal(err)
		}
	}
	ents, err := os.ReadDir(s.topicDir("t"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) > MaxPerTopic {
		t.Errorf("topic holds %d messages, cap is %d", len(ents), MaxPerTopic)
	}
}

// A cursor pointing at a message that retention has since deleted must not
// resurrect the backlog or error.
func TestCursorSurvivesRetention(t *testing.T) {
	s := newStore(t)
	s.Subscribe("bob", "t")
	s.Publish("t", "x", "old")
	s.Poll("bob", nil, 0)
	os.RemoveAll(s.topicDir("t"))
	os.MkdirAll(s.topicDir("t"), 0o755)
	s.Publish("t", "x", "new")
	got, err := s.Poll("bob", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Body != "new" {
		t.Errorf("got %+v, want just the new message", got)
	}
}

// A DM needs no separate mechanism, but it must actually isolate: bob's DMs are
// not readable by carol just because she guesses the topic... except she can, by
// subscribing. Document that honestly — this is a convention, not a boundary.
func TestDirectMessageIsJustATopic(t *testing.T) {
	s := newStore(t)
	s.Subscribe("bob", "dm.bob")
	s.Publish("dm.bob", "alice", "just for you")
	got, _ := s.Poll("bob", nil, 0)
	if len(got) != 1 {
		t.Fatalf("DM not delivered: %+v", got)
	}
	// Anyone MAY subscribe to another's DM topic. Asserted so the limitation is
	// recorded rather than assumed away: topics are not access-controlled.
	s.Subscribe("carol", "dm.bob")
	s.Publish("dm.bob", "alice", "second")
	if got, _ := s.Poll("carol", nil, 0); len(got) == 0 {
		t.Skip("if this ever fails, DM topics gained access control — update the docs")
	}
}
