package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// list_tools: the catalog, as a TOOL CALL rather than as introspection.
//
// WHY THIS EXISTS. Asking a model what tools it has does not read its tool declarations -- it
// answers from context, and context is frequently wrong. Observed 2026-09-21 on voicebox: the
// catalog went from 6 tools to 13, the server logged "WITH 13 tools offered" for every request,
// and the assistant reported six three times running, reasoning explicitly that it "can't probe
// for them". It was not being careless. Its own earlier message listing six was in the
// conversation, and it had just written a memory entry recording six, so the wrong answer was
// the better-attested one. It only stopped being wrong when it CALLED something new.
//
// A tool result is different in kind from a declaration: it arrives as content the model has
// just observed, at the position in context where it is being asked. So the one reliable way
// for a model to answer "what can I do" is to look it up, which is what this provides.
//
// It reports what THIS registry exposes, read at call time, so a --tools whitelist is reflected
// automatically and a restricted instance cannot advertise tools it would refuse to run.
//
// Deliberately NOT exempt from the whitelist. An instance that wants it names it, like every
// other tool -- "a caller who wants five tools should name five tools" is the rule the
// whitelist was built on, and a meta-tool is not a good enough reason to put a hole in it.
func registerListTools(r *Registry) {
	r.Register(&Tool{
		Name: "list_tools",
		Description: "List the tools you actually have right now, with their descriptions. " +
			"Use this whenever you are asked what you can do, or whether your tools have " +
			"changed: your own recollection of your tool list is not reliable, and this is.",
		Schema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"contains": map[string]interface{}{
					"type":        "string",
					"description": "Optional: only tools whose name or description contains this text.",
				},
			},
		},
		ReadOnly: true,
		Handler: func(ctx context.Context, args map[string]interface{}) (string, error) {
			filter, _ := args["contains"].(string)
			filter = strings.ToLower(strings.TrimSpace(filter))

			defs := r.ListTools()
			names := make([]string, 0, len(defs))
			desc := map[string]string{}
			for _, d := range defs {
				if filter != "" &&
					!strings.Contains(strings.ToLower(d.Name), filter) &&
					!strings.Contains(strings.ToLower(d.Description), filter) {
					continue
				}
				names = append(names, d.Name)
				// One line each: the full description of 13 tools is most of a short context
				// window, and the caller can ask about a specific tool if it needs more.
				// Cut at a sentence end, which is a period followed by SPACE -- not a bare
				// period, which lives inside "MEMORY.md" and truncated three descriptions
				// mid-filename on the first run.
				first := d.Description
				if i := strings.IndexAny(first, "\n"); i > 0 {
					first = first[:i]
				}
				if i := strings.Index(first, ". "); i > 0 {
					first = first[:i]
				}
				desc[d.Name] = strings.TrimSpace(first)
			}
			sort.Strings(names)

			if len(names) == 0 {
				if filter != "" {
					return fmt.Sprintf("No tools match %q. Call list_tools with no arguments "+
						"to see everything available.", filter), nil
				}
				return "No tools are available.", nil
			}
			var b strings.Builder
			if filter != "" {
				fmt.Fprintf(&b, "%d of %d tools match %q:\n", len(names), len(defs), filter)
			} else {
				fmt.Fprintf(&b, "You have %d tools:\n", len(names))
			}
			for _, n := range names {
				fmt.Fprintf(&b, "  %-18s %s\n", n, desc[n])
			}
			return strings.TrimRight(b.String(), "\n"), nil
		},
	})
}
