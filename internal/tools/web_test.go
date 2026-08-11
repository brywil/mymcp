package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

// These cover the mode dispatch itself — the behaviour that changed when
// web_search_small/full/raw were consolidated into one tool. The previous
// version of this file asserted only the shape of the schema map, which would
// have passed just as happily if the handler ignored `mode` entirely.

func sampleResponse() *ollamaWebSearchResponse {
	return &ollamaWebSearchResponse{Results: []ollamaWebSearchResult{
		{Title: "First Result", URL: "https://example.com/1", Content: "alpha content"},
		{Title: "Second Result", URL: "https://example.com/2", Content: "beta content"},
	}}
}

// small: titles and URLs, and explicitly NOT the content — the whole point of
// the mode is keeping big page bodies out of the model's context.
func TestFormatSearchResultsSmallOmitsContent(t *testing.T) {
	out, err := formatSearchResults("q", sampleResponse(), "small")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"First Result", "https://example.com/1", "Second Result"} {
		if !strings.Contains(out, want) {
			t.Errorf("small output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "alpha content") {
		t.Errorf("small mode leaked page content — that defeats its purpose:\n%s", out)
	}
	// The pointer to the richer mode must name the parameter, not the retired
	// tool: a stale "use web_search_full" sends the model after something gone.
	if !strings.Contains(out, "mode='full'") {
		t.Errorf("small output should point at mode='full', got:\n%s", out)
	}
}

// full: same results, but carrying the content snippets.
func TestFormatSearchResultsFullIncludesContent(t *testing.T) {
	out, err := formatSearchResults("q", sampleResponse(), "full")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"First Result", "https://example.com/2", "alpha content", "beta content"} {
		if !strings.Contains(out, want) {
			t.Errorf("full output missing %q:\n%s", want, out)
		}
	}
}

// raw: parseable JSON that round-trips, not a prose rendering.
func TestFormatSearchResultsRawIsJSON(t *testing.T) {
	out, err := formatSearchResults("q", sampleResponse(), "raw")
	if err != nil {
		t.Fatal(err)
	}
	var back ollamaWebSearchResponse
	if err := json.Unmarshal([]byte(out), &back); err != nil {
		t.Fatalf("raw mode did not produce valid JSON: %v\n%s", err, out)
	}
	if len(back.Results) != 2 || back.Results[0].Title != "First Result" {
		t.Errorf("raw mode lost data on the round trip: %+v", back.Results)
	}
}

// The three modes must actually differ. A handler that ignored `mode` would
// pass each test above in isolation but not this one.
func TestFormatSearchResultsModesDiffer(t *testing.T) {
	seen := map[string]string{}
	for _, m := range searchModes {
		out, err := formatSearchResults("q", sampleResponse(), m)
		if err != nil {
			t.Fatal(err)
		}
		for prev, prevOut := range seen {
			if prevOut == out {
				t.Errorf("mode %q produced identical output to %q", m, prev)
			}
		}
		seen[m] = out
	}
}

func TestValidSearchMode(t *testing.T) {
	for _, m := range searchModes {
		if !validSearchMode(m) {
			t.Errorf("%q should be valid", m)
		}
	}
	for _, m := range []string{"Full", "verbose", "", "smal"} {
		if validSearchMode(m) {
			t.Errorf("%q should not be valid", m)
		}
	}
}

// The schema's enum and the validator must be the same list, or a mode the
// schema advertises gets rejected at runtime.
func TestSchemaEnumMatchesValidator(t *testing.T) {
	props, ok := webSearchSchema["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("schema has no properties")
	}
	mode, ok := props["mode"].(map[string]interface{})
	if !ok {
		t.Fatal("schema has no mode property")
	}
	enum, ok := mode["enum"].([]string)
	if !ok {
		t.Fatalf("mode enum is not a []string: %T", mode["enum"])
	}
	if len(enum) != len(searchModes) {
		t.Fatalf("enum %v and validator %v disagree", enum, searchModes)
	}
	for _, e := range enum {
		if !validSearchMode(e) {
			t.Errorf("schema advertises %q but the validator rejects it", e)
		}
	}
}
