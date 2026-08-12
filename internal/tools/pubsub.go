package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/brywil/mymcp/internal/pubsub"
)

// -----------------------------------------------------------------------------

// agentTools let agents talk to each other: publish a question or a result to a
// topic, and read what others have published when you next take a turn.
//
// The intended use is collaboration — get stuck, ask a stronger model, carry on
// while it thinks. That shape dictates the design:
//
//   - NOTHING BLOCKS BY DEFAULT. agent_poll returns immediately. A tool call that
//     sat waiting for a reply would stall the agent's whole tool-calling loop and
//     make the feature worse than not having it. Waiting is opt-in via
//     timeout_seconds, and reuses wait_for's semantics: a clamped ceiling and a
//     result that cannot be mistaken for success.
//   - IDENTITY IS NOT AN ARGUMENT. The sender is the authenticated principal, so
//     an agent cannot publish as another without holding its bearer token.
//   - READS ARE NON-DESTRUCTIVE. Every subscriber has its own cursor, so asking
//     two agents for help reaches both.
//
// Not a security boundary: topics are not access-controlled, so any agent may
// subscribe to any topic including another's "dm.<name>". Treat topics as a
// convention for routing, not for confidentiality.
type agentTools struct {
	store *pubsub.FileStore
	self  func(ctx context.Context) string
}

// RegisterAgentTools installs the agent_* messaging tools into r, backed by a
// store at base. Exported because messaging lives in its OWN server (myagent),
// not in the local-tools server: the message store may sit on a shared mount, and
// a stall there must not take file and shell tools down with it.
func RegisterAgentTools(r *Registry, base string) error {
	at, err := newAgentTools(base)
	if err != nil {
		return err
	}
	at.register(r)
	return nil
}

func newAgentTools(base string) (*agentTools, error) {
	s, err := pubsub.NewFileStore(base)
	if err != nil {
		return nil, err
	}
	return &agentTools{
		store: s,
		self:  func(ctx context.Context) string { return memoryPrincipal(ctx) },
	}, nil
}

func (at *agentTools) register(r *Registry) {
	r.Register(&Tool{
		Name: "agent_publish",
		Description: "Send a message to a topic that other agents can read. Use to ask a stronger " +
			"model for help when stuck, share a result, or coordinate work. Returns immediately; " +
			"replies arrive via agent_poll on a later turn. To message one agent directly, publish " +
			"to the topic \"dm.<their-name>\" (see agent_list for names).",
		Schema: obj(map[string]interface{}{
			"topic":   strProp("Topic to publish to, e.g. \"help\" or \"dm.opus\". Lowercased; letters, digits, - _ . only."),
			"message": strProp("What to say. Include enough context to be actionable on its own — the reader has not seen your conversation."),
		}, "topic", "message"),
		Handler: at.publish,
	})
	r.Register(&Tool{
		Name: "agent_subscribe",
		Description: "Start receiving messages published to a topic. Subscriptions persist across " +
			"restarts. Subscribe to \"dm.<your-name>\" to receive direct messages.",
		Schema:  obj(map[string]interface{}{"topic": strProp("Topic to subscribe to")}, "topic"),
		Handler: at.subscribe,
	})
	r.Register(&Tool{
		Name:        "agent_unsubscribe",
		Description: "Stop receiving messages from a topic.",
		Schema:      obj(map[string]interface{}{"topic": strProp("Topic to unsubscribe from")}, "topic"),
		Handler:     at.unsubscribe,
	})
	r.Register(&Tool{
		Name: "agent_poll",
		Description: "Read new messages addressed to you. Returns IMMEDIATELY by default — call it " +
			"whenever you want to check for replies, including at the start of a turn. Each message " +
			"is delivered once. Set timeout_seconds only when you genuinely intend to wait.",
		Schema: obj(map[string]interface{}{
			"topics":          strProp("Comma-separated topics to read. Default: everything you subscribe to."),
			"timeout_seconds": map[string]interface{}{"type": "integer", "description": "Wait up to this long for a message to arrive (default 0 = return immediately, max 300)."},
			"max":             map[string]interface{}{"type": "integer", "description": "Maximum messages to return (default 20)."},
			"peek":            map[string]interface{}{"type": "boolean", "description": "Read without consuming, so the same messages are returned again next time (default false)."},
		}),
		ReadOnly: true,
		Handler:  at.poll,
	})
	r.Register(&Tool{
		Name: "agent_list",
		Description: "List agents that have been active recently, with the topics each subscribes to. " +
			"Use to find out who is available before asking for help, and to get exact names for " +
			"\"dm.<name>\" topics.",
		Schema: obj(map[string]interface{}{
			"within_minutes": map[string]interface{}{"type": "integer", "description": "Only list agents seen this recently (default 60; 0 = all ever seen)."},
		}),
		ReadOnly: true,
		Handler:  at.list,
	})
}

// me resolves the caller, refusing when the server cannot tell agents apart.
// Falling back to a shared identity would let one agent read another's messages
// and publish under its name, so this fails rather than degrading.
func (at *agentTools) me(ctx context.Context) (string, error) {
	p := at.self(ctx)
	if p == "" || p == "shared" {
		return "", errors.New("this server cannot identify you (no authenticated principal), so agent messaging is disabled. " +
			"Start mymcp with bearer tokens and connect with your own named token")
	}
	_ = at.store.Touch(p)
	return p, nil
}

func (at *agentTools) publish(ctx context.Context, a map[string]interface{}) (string, error) {
	me, err := at.me(ctx)
	if err != nil {
		return "", err
	}
	topic := argString(a, "topic")
	body := argString(a, "message")
	if strings.TrimSpace(body) == "" {
		return "", errors.New("message is empty")
	}
	t, ok := pubsub.ValidTopic(topic)
	if !ok {
		return "", fmt.Errorf("invalid topic %q", topic)
	}

	// A DM to a name nobody has ever used is a typo, and silently accepting it is
	// how a request vanishes with no error and no reply. Say so, and say who exists.
	if name := strings.TrimPrefix(t, "dm."); name != t {
		known, _ := at.store.KnownAgents()
		if len(known) > 0 && !contains(known, name) {
			return "", fmt.Errorf("no agent named %q has ever connected to this server; known agents: %s. "+
				"Check agent_list, or publish to a shared topic instead", name, strings.Join(known, ", "))
		}
	}

	m, err := at.store.Publish(t, me, body)
	if err != nil {
		return "", err
	}
	subs := at.subscriberCount(t)
	var b strings.Builder
	fmt.Fprintf(&b, "Published to %q as %q.\n", t, me)
	switch subs {
	case 0:
		// Not an error: publishing before anyone subscribes is legitimate, and the
		// message is retained. But the caller must not read this as "delivered".
		fmt.Fprintf(&b, "No agent currently subscribes to %q, so nobody will see this until one does. "+
			"The message is retained. Check agent_list for who is active.\n", t)
	case 1:
		b.WriteString("1 agent subscribes to this topic.\n")
	default:
		fmt.Fprintf(&b, "%d agents subscribe to this topic.\n", subs)
	}
	fmt.Fprintf(&b, "\nReplies do not arrive automatically — call agent_poll on a later turn to read them. "+
		"Message id %s.", m.ID)
	return b.String(), nil
}

func (at *agentTools) subscriberCount(topic string) int {
	ags, err := at.store.Agents(0)
	if err != nil {
		return 0
	}
	n := 0
	for _, ag := range ags {
		if contains(ag.Topics, topic) {
			n++
		}
	}
	return n
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func (at *agentTools) subscribe(ctx context.Context, a map[string]interface{}) (string, error) {
	me, err := at.me(ctx)
	if err != nil {
		return "", err
	}
	t, ok := pubsub.ValidTopic(argString(a, "topic"))
	if !ok {
		return "", fmt.Errorf("invalid topic %q", argString(a, "topic"))
	}
	if err := at.store.Subscribe(me, t); err != nil {
		return "", err
	}
	subs, _ := at.store.Subscriptions(me)
	return fmt.Sprintf("Subscribed %q to %q. You now subscribe to: %s.\nMessages do not push — call agent_poll to read them.",
		me, t, strings.Join(subs, ", ")), nil
}

func (at *agentTools) unsubscribe(ctx context.Context, a map[string]interface{}) (string, error) {
	me, err := at.me(ctx)
	if err != nil {
		return "", err
	}
	t, _ := pubsub.ValidTopic(argString(a, "topic"))
	if err := at.store.Unsubscribe(me, t); err != nil {
		return "", err
	}
	subs, _ := at.store.Subscriptions(me)
	if len(subs) == 0 {
		return fmt.Sprintf("Unsubscribed from %q. You now subscribe to nothing.", t), nil
	}
	return fmt.Sprintf("Unsubscribed from %q. Still subscribed to: %s.", t, strings.Join(subs, ", ")), nil
}

const maxPollWait = 300

func (at *agentTools) poll(ctx context.Context, a map[string]interface{}) (string, error) {
	me, err := at.me(ctx)
	if err != nil {
		return "", err
	}
	var topics []string
	for _, t := range strings.Split(argString(a, "topics"), ",") {
		if t = strings.TrimSpace(t); t != "" {
			topics = append(topics, t)
		}
	}
	max := argInt(a, "max", 20)
	peek := argBool(a, "peek", false)
	wait := argInt(a, "timeout_seconds", 0)
	if wait > maxPollWait {
		wait = maxPollWait
	}

	read := at.store.Poll
	if peek {
		read = at.store.Peek
	}

	deadline := time.Now().Add(time.Duration(wait) * time.Second)
	for {
		ms, err := read(me, topics, max)
		if err != nil {
			if errors.Is(err, pubsub.ErrNoSuchTopic) {
				subs, _ := at.store.Subscriptions(me)
				if len(subs) == 0 {
					return "", fmt.Errorf("%w. You subscribe to nothing — call agent_subscribe first", err)
				}
				return "", fmt.Errorf("%w. You subscribe to: %s", err, strings.Join(subs, ", "))
			}
			return "", err
		}
		if len(ms) > 0 {
			return renderMessages(ms, peek), nil
		}
		if wait <= 0 || !time.Now().Before(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			return "Cancelled while waiting for messages. Nothing was consumed.", nil
		case <-time.After(time.Second):
		}
	}

	subs, _ := at.store.Subscriptions(me)
	if len(subs) == 0 {
		return "No messages. You subscribe to no topics — call agent_subscribe first, " +
			"or agent_list to see what other agents are listening to.", nil
	}
	if wait > 0 {
		// Distinguish "waited and nothing came" from "checked and nothing was
		// there", so a caller cannot read a timeout as a delivered empty inbox.
		return fmt.Sprintf("No messages after waiting %ds on: %s. Nothing arrived — this is not a delivery failure, "+
			"just silence. Try again later or check agent_list for who is active.", wait, strings.Join(subs, ", ")), nil
	}
	return fmt.Sprintf("No new messages on: %s.", strings.Join(subs, ", ")), nil
}

func renderMessages(ms []pubsub.Message, peek bool) string {
	var b strings.Builder
	verb := "New"
	if peek {
		verb = "Pending (not consumed; will be returned again)"
	}
	fmt.Fprintf(&b, "%s message(s): %d\n", verb, len(ms))
	for _, m := range ms {
		fmt.Fprintf(&b, "\n--- from %s on %q at %s ---\n%s\n",
			m.From, m.Topic, m.Time.Format(time.RFC3339), m.Body)
	}
	if !peek {
		b.WriteString("\nThese will not be returned again. Reply with agent_publish to \"dm.<sender>\" or the same topic.")
	}
	return b.String()
}

func (at *agentTools) list(ctx context.Context, a map[string]interface{}) (string, error) {
	me, _ := at.me(ctx) // listing is useful even unauthenticated; ignore the error
	within := time.Duration(argInt(a, "within_minutes", 60)) * time.Minute
	ags, err := at.store.Agents(within)
	if err != nil {
		return "", err
	}
	if len(ags) == 0 {
		known, _ := at.store.KnownAgents()
		if len(known) == 0 {
			return "No agents have ever connected to this server.", nil
		}
		return fmt.Sprintf("No agents active in the last %s. Seen previously: %s.",
			within, strings.Join(known, ", ")), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Agents active in the last %s:\n", within)
	now := time.Now()
	for _, ag := range ags {
		who := ag.Principal
		if who == me {
			who += " (you)"
		}
		topics := "no subscriptions"
		if len(ag.Topics) > 0 {
			topics = strings.Join(ag.Topics, ", ")
		}
		fmt.Fprintf(&b, "  %-20s last seen %-12s subscribes: %s\n",
			who, now.Sub(ag.LastSeen).Truncate(time.Second).String()+" ago", topics)
	}
	b.WriteString("\nMessage one directly by publishing to \"dm.<name>\".")
	return b.String(), nil
}
