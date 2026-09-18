package tools

import (
	"fmt"
	"sort"
	"strings"
)

// Capability selection: which tools a server instance exposes.
//
// The primary mechanism is an explicit WHITELIST, because that is the honest
// shape of the requirement. A caller who wants five tools should name five
// tools; expressing that as "a preset, minus the parts I don't want, plus the
// parts it missed" is harder to read and harder to audit, and the failure mode
// is silent over-exposure.
//
// Two preset words are accepted inside the list for convenience:
//
//	all  every registered tool (the default when nothing is specified)
//	ro   every tool flagged ReadOnly — a tag that predates this file by a long
//	     way, described in the Tool struct as "eligible for the ro capability
//	     preset", and which until now nothing consumed
//
// So `--tools ro,memory_save` reads as "everything read-only, plus the ability
// to remember" — which is the assistant case, since memory_save is a write and
// therefore outside ro.
func ResolveTools(r *Registry, spec []string) ([]string, error) {
	set := map[string]bool{}
	for _, raw := range spec {
		for _, item := range strings.Split(raw, ",") {
			name := strings.TrimSpace(item)
			if name == "" {
				continue
			}
			switch strings.ToLower(name) {
			case "all":
				for _, n := range r.Names() {
					set[n] = true
				}
				continue
			case "ro", "readonly", "read-only":
				for _, n := range r.Names() {
					if r.ReadOnly(n) {
						set[n] = true
					}
				}
				continue
			}
			// A typo must be an error, not a silent omission: the operator
			// believes the tool is available and would only discover otherwise
			// when a model tries to use it, by which point the failure looks
			// like the model's fault.
			if !r.Has(name) {
				return nil, fmt.Errorf("--tools %q: no such tool (see `mymcp tools` for the catalog)", name)
			}
			set[name] = true
		}
	}
	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

// Has reports whether a tool is registered, regardless of restriction.
func (r *Registry) Has(name string) bool {
	_, ok := r.tools[name]
	return ok
}
