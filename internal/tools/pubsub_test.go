package tools

import (
	"context"
	"strings"
	"testing"
	"time"
)

// newAgentPair builds two agents sharing one store, which is the real topology:
// several agents, one directory.
func newAgentPair(t *testing.T) (*agentTools, context.Context, context.Context) {
	t.Helper()
	at, err := newAgentTools(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Identity normally comes from the bearer token via the request context; stub
	// the resolver so tests can act as different principals.
	who := map[context.Context]string{}
	a := context.WithValue(context.Background(), ctxKey("who"), "alice")
	b := context.WithValue(context.Background(), ctxKey("who"), "bob")
	who[a], who[b] = "alice", "bob"
	at.self = func(ctx context.Context) string {
		if v, ok := ctx.Value(ctxKey("who")).(string); ok {
			return v
		}
		return ""
	}
	return at, a, b
}

type ctxKey string

func args(kv ...interface{}) map[string]interface{} {
	m := map[string]interface{}{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	return m
}

// The headline property: polling must NOT block by default. A tool that sat
// waiting would stall the agent's whole tool-calling loop, which is exactly what
// this design set out to avoid.
func TestPollReturnsImmediatelyByDefault(t *testing.T) {
	at, a, _ := newAgentPair(t)
	if _, err := at.subscribe(a, args("topic", "help")); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	out, err := at.poll(a, args())
	if err != nil {
		t.Fatal(err)
	}
	if el := time.Since(start); el > time.Second {
		t.Errorf("poll took %s with no timeout set; it must return immediately", el)
	}
	if !strings.Contains(out, "No new messages") {
		t.Errorf("unexpected: %s", out)
	}
}

func TestPublishThenOtherAgentPolls(t *testing.T) {
	at, alice, bob := newAgentPair(t)
	at.subscribe(bob, args("topic", "help"))
	out, err := at.publish(alice, args("topic", "help", "message", "stuck on a name map"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "1 agent subscribes") {
		t.Errorf("subscriber count not reported: %s", out)
	}
	got, err := at.poll(bob, args())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "stuck on a name map") || !strings.Contains(got, "alice") {
		t.Errorf("message or sender missing: %s", got)
	}
}

// Publishing where nobody is listening is legitimate, but must never read as
// "delivered" — that is how a request for help vanishes silently.
func TestPublishWithNoSubscribersSaysSo(t *testing.T) {
	at, alice, _ := newAgentPair(t)
	out, err := at.publish(alice, args("topic", "void", "message", "anyone?"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "No agent currently subscribes") {
		t.Errorf("silent publish into the void: %s", out)
	}
	if !strings.Contains(out, "retained") {
		t.Errorf("should say the message is kept: %s", out)
	}
}

// A DM to a misspelled name is the most likely mistake, and the one that would
// otherwise fail silently. It must name who actually exists.
func TestDirectMessageToUnknownAgentIsLoud(t *testing.T) {
	at, alice, bob := newAgentPair(t)
	at.subscribe(bob, args("topic", "x")) // bob becomes known
	_, err := at.publish(alice, args("topic", "dm.bobb", "message", "hi"))
	if err == nil {
		t.Fatal("DM to a nonexistent agent was accepted")
	}
	if !strings.Contains(err.Error(), "bobb") || !strings.Contains(err.Error(), "bob") {
		t.Errorf("error should name the typo AND the real agents: %v", err)
	}
}

func TestDirectMessageToKnownAgentWorks(t *testing.T) {
	at, alice, bob := newAgentPair(t)
	at.subscribe(bob, args("topic", "dm.bob"))
	if _, err := at.publish(alice, args("topic", "dm.bob", "message", "just for you")); err != nil {
		t.Fatal(err)
	}
	got, _ := at.poll(bob, args())
	if !strings.Contains(got, "just for you") {
		t.Errorf("DM not delivered: %s", got)
	}
}

// Without a distinguishable identity, messaging must refuse rather than pooling
// every caller into one shared mailbox.
func TestUnidentifiedCallerIsRefused(t *testing.T) {
	at, _, _ := newAgentPair(t)
	anon := context.Background()
	for name, fn := range map[string]func(context.Context, map[string]interface{}) (string, error){
		"publish":   at.publish,
		"subscribe": at.subscribe,
		"poll":      at.poll,
	} {
		if _, err := fn(anon, args("topic", "t", "message", "m")); err == nil {
			t.Errorf("%s allowed an unidentified caller", name)
		} else if !strings.Contains(err.Error(), "identify") {
			t.Errorf("%s error should explain identity is missing: %v", name, err)
		}
	}
}

// A wait that expires must not read like an empty inbox; the caller has to be
// able to tell "nothing arrived" from "I did not look".
func TestTimeoutIsDistinguishableFromNoMessages(t *testing.T) {
	at, a, _ := newAgentPair(t)
	at.subscribe(a, args("topic", "quiet"))

	instant, _ := at.poll(a, args())
	waited, _ := at.poll(a, args("timeout_seconds", 1))

	if instant == waited {
		t.Error("waiting and not waiting produced identical text")
	}
	if !strings.Contains(waited, "after waiting") {
		t.Errorf("timeout result should say it waited: %s", waited)
	}
	if !strings.Contains(waited, "not a delivery failure") {
		t.Errorf("timeout should not read as an error: %s", waited)
	}
}

// Messages are delivered once; re-polling every turn must not re-answer the same
// question forever.
func TestPollConsumes(t *testing.T) {
	at, alice, bob := newAgentPair(t)
	at.subscribe(bob, args("topic", "t"))
	at.publish(alice, args("topic", "t", "message", "one"))
	if got, _ := at.poll(bob, args()); !strings.Contains(got, "one") {
		t.Fatal("first poll missed it")
	}
	if got, _ := at.poll(bob, args()); strings.Contains(got, "one") {
		t.Error("message redelivered")
	}
}

func TestPeekDoesNotConsume(t *testing.T) {
	at, alice, bob := newAgentPair(t)
	at.subscribe(bob, args("topic", "t"))
	at.publish(alice, args("topic", "t", "message", "one"))
	got, _ := at.poll(bob, args("peek", true))
	if !strings.Contains(got, "not consumed") {
		t.Errorf("peek should say it did not consume: %s", got)
	}
	if got, _ := at.poll(bob, args()); !strings.Contains(got, "one") {
		t.Error("peek consumed the message")
	}
}

// Polling a topic that never existed is a typo, and must say so plus list what
// the caller actually subscribes to.
func TestPollUnknownTopicIsLoud(t *testing.T) {
	at, a, _ := newAgentPair(t)
	at.subscribe(a, args("topic", "help"))
	_, err := at.poll(a, args("topics", "hlep"))
	if err == nil {
		t.Fatal("unknown topic returned success")
	}
	if !strings.Contains(err.Error(), "help") {
		t.Errorf("error should list real subscriptions: %v", err)
	}
}

func TestListShowsAgentsAndTopics(t *testing.T) {
	at, alice, bob := newAgentPair(t)
	at.subscribe(alice, args("topic", "help"))
	at.subscribe(bob, args("topic", "builds"))
	out, err := at.list(alice, args())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"alice", "bob", "help", "builds", "(you)"} {
		if !strings.Contains(out, want) {
			t.Errorf("listing missing %q: %s", want, out)
		}
	}
}

func TestEmptyMessageRejected(t *testing.T) {
	at, alice, _ := newAgentPair(t)
	if _, err := at.publish(alice, args("topic", "t", "message", "   ")); err == nil {
		t.Error("empty message accepted")
	}
}

// The wait ceiling is clamped so a caller cannot pin a tool slot indefinitely.
func TestWaitIsClamped(t *testing.T) {
	if maxPollWait > 300 {
		t.Errorf("max wait %d is too long for a tool that holds a slot", maxPollWait)
	}
}

// Cancellation must be honoured promptly, or a long wait outlives the turn.
func TestPollHonoursCancellation(t *testing.T) {
	at, _, _ := newAgentPair(t)
	base := context.WithValue(context.Background(), ctxKey("who"), "alice")
	at.subscribe(base, args("topic", "quiet"))
	ctx, cancel := context.WithCancel(base)
	go func() { time.Sleep(300 * time.Millisecond); cancel() }()
	start := time.Now()
	out, err := at.poll(ctx, args("timeout_seconds", 60))
	if err != nil {
		t.Fatal(err)
	}
	if el := time.Since(start); el > 5*time.Second {
		t.Errorf("cancellation ignored: waited %s", el)
	}
	if !strings.Contains(out, "Cancelled") {
		t.Errorf("cancellation not reported: %s", out)
	}
}
