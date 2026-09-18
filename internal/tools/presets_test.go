package tools

import (
	"context"
	"strings"
	"testing"
)

func testRegistry() *Registry {
	r := NewRegistry()
	add := func(name string, ro bool) {
		r.Register(&Tool{Name: name, ReadOnly: ro,
			Handler: func(context.Context, map[string]interface{}) (string, error) {
				return "ran " + name, nil
			}})
	}
	add("web_search_free", true)
	add("memory_search", true)
	add("memory_save", false) // a write: outside `ro`
	add("date_now", true)
	add("run_command", false)
	add("http_request", false)
	return r
}

// THE ONE THAT MATTERS. Restricting the listing alone is theatre: a catalog is a
// hint, and a model that has seen a tool name anywhere can just ask for it. If
// dispatch does not enforce the whitelist, the whitelist is decorative.
func TestRestrictIsEnforcedOnDispatchNotJustListing(t *testing.T) {
	r := testRegistry()
	r.Restrict([]string{"web_search_free", "date_now"})

	if out, isErr := r.CallTool(context.Background(), "run_command", nil); !isErr {
		t.Fatalf("a tool outside the whitelist RAN: %q", out)
	}
	// ...and the permitted one still works, or the restriction is just a brick.
	if out, isErr := r.CallTool(context.Background(), "web_search_free", nil); isErr {
		t.Fatalf("a whitelisted tool was refused: %q", out)
	}
}

func TestRestrictHidesFromListing(t *testing.T) {
	r := testRegistry()
	r.Restrict([]string{"web_search_free", "memory_save"})
	var names []string
	for _, d := range r.ListTools() {
		names = append(names, d.Name)
	}
	if len(names) != 2 {
		t.Fatalf("listed %v, want exactly the two whitelisted", names)
	}
	for _, n := range names {
		if n != "web_search_free" && n != "memory_save" {
			t.Errorf("leaked %q into the listing", n)
		}
	}
}

// An unrestricted registry must behave exactly as before — this is the default
// every existing deployment runs on.
func TestUnrestrictedRegistryExposesEverything(t *testing.T) {
	r := testRegistry()
	if got := len(r.ListTools()); got != 6 {
		t.Errorf("listed %d tools, want all 6", got)
	}
	if _, isErr := r.CallTool(context.Background(), "run_command", nil); isErr {
		t.Error("unrestricted registry refused a tool")
	}
}

func TestResolveToolsExplicitWhitelist(t *testing.T) {
	r := testRegistry()
	got, err := ResolveTools(r, []string{"web_search_free,date_now"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, " ") != "date_now web_search_free" {
		t.Errorf("got %v", got)
	}
}

// `ro` resolves from the ReadOnly tags, and memory_save is deliberately NOT in
// it — writing is a write. That is exactly why the spec allows adding names.
func TestResolveToolsRoPresetExcludesWritesAndCanBeExtended(t *testing.T) {
	r := testRegistry()
	ro, err := ResolveTools(r, []string{"ro"})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range ro {
		if n == "memory_save" || n == "run_command" || n == "http_request" {
			t.Errorf("`ro` included the write tool %q", n)
		}
	}
	withSave, err := ResolveTools(r, []string{"ro,memory_save"})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, n := range withSave {
		if n == "memory_save" {
			found = true
		}
	}
	if !found {
		t.Error("ro,memory_save did not include memory_save")
	}
	if len(withSave) != len(ro)+1 {
		t.Errorf("expected exactly one addition, got %v vs %v", withSave, ro)
	}
}

// A typo must fail loudly. Silently exposing nothing would look like the tool
// being broken at call time, and the operator would blame the model.
func TestResolveToolsRejectsAnUnknownName(t *testing.T) {
	r := testRegistry()
	if _, err := ResolveTools(r, []string{"web_search_fre"}); err == nil {
		t.Fatal("a misspelled tool name was accepted")
	} else if !strings.Contains(err.Error(), "no such tool") {
		t.Errorf("unhelpful error: %v", err)
	}
}
