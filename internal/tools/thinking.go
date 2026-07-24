package tools

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
)

// thinkTools provides `sequentialthinking` — a reasoning scratchpad ported from
// @modelcontextprotocol/server-sequential-thinking. It runs no model; it just
// records each thought and echoes the running state, forcing a model to
// externalize step-by-step reasoning (mainly useful for smaller/weaker models).
// History is namespaced per authenticated principal and lives in memory only.
type thinkTools struct {
	mu       sync.Mutex
	count    map[string]int             // principal -> thoughts recorded in the current chain
	branches map[string]map[string]bool // principal -> set of branch ids
}

func newThinkTools() *thinkTools {
	return &thinkTools{count: map[string]int{}, branches: map[string]map[string]bool{}}
}

func (t *thinkTools) register(r *Registry) {
	r.Register(&Tool{
		Name: "sequentialthinking",
		Description: "A tool for dynamic, reflective, step-by-step problem-solving. " +
			"Record one thought per call; set nextThoughtNeeded=false when you've reached a satisfactory answer. " +
			"You can revise earlier thoughts (isRevision + revisesThought) or branch (branchFromThought + branchId). " +
			"Use it to work through multi-step problems before giving a final answer.",
		Schema: obj(map[string]interface{}{
			"thought":           strProp("Your current thinking step"),
			"nextThoughtNeeded": map[string]interface{}{"type": "boolean", "description": "Whether another thought step is needed"},
			"thoughtNumber":     map[string]interface{}{"type": "integer", "description": "Current thought number (starts at 1; 1 begins a fresh chain)"},
			"totalThoughts":     map[string]interface{}{"type": "integer", "description": "Current estimate of total thoughts needed"},
			"isRevision":        map[string]interface{}{"type": "boolean", "description": "Whether this revises previous thinking"},
			"revisesThought":    map[string]interface{}{"type": "integer", "description": "Which thought number is being reconsidered"},
			"branchFromThought": map[string]interface{}{"type": "integer", "description": "Thought number this branches from"},
			"branchId":          strProp("Branch identifier"),
			"needsMoreThoughts": map[string]interface{}{"type": "boolean", "description": "If more thoughts are needed than the current estimate"},
		}, "thought", "nextThoughtNeeded", "thoughtNumber", "totalThoughts"),
		ReadOnly: true,
		Handler:  t.think,
	})
}

func (t *thinkTools) think(ctx context.Context, a map[string]interface{}) (string, error) {
	if argString(a, "thought") == "" {
		return "", errors.New("thought is required and must be a non-empty string")
	}
	p := memoryPrincipal(ctx)
	num := argInt(a, "thoughtNumber", 0)

	t.mu.Lock()
	// thoughtNumber == 1 starts a fresh reasoning chain for this principal.
	if num <= 1 {
		t.count[p] = 0
		t.branches[p] = map[string]bool{}
	}
	t.count[p]++
	if bid := argString(a, "branchId"); bid != "" {
		if t.branches[p] == nil {
			t.branches[p] = map[string]bool{}
		}
		t.branches[p][bid] = true
	}
	branchList := make([]string, 0, len(t.branches[p]))
	for b := range t.branches[p] {
		branchList = append(branchList, b)
	}
	histLen := t.count[p]
	t.mu.Unlock()

	out := map[string]interface{}{
		"thoughtNumber":        argInt(a, "thoughtNumber", histLen),
		"totalThoughts":        argInt(a, "totalThoughts", histLen),
		"nextThoughtNeeded":    argBool(a, "nextThoughtNeeded", false),
		"branches":             branchList,
		"thoughtHistoryLength": histLen,
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}
