package tools

import (
	"os"
	"testing"
)

// The built-in list is empty (see the comment on wedgeHosts: the sites it named were
// broken by this machine's own browser flags, not by the sites). So the matching rules
// are exercised through the override, which is now the only way a host gets on the list.
func TestWedgeHostMatching(t *testing.T) {
	t.Setenv("MYMCP_WEDGE_HOSTS", "wedgy.example")
	cases := map[string]bool{
		"https://wedgy.example/a":           true,
		"https://WEDGY.EXAMPLE/a":           true, // case-insensitive
		"https://cdn.wedgy.example/f.gguf":  true, // subdomain
		"https://github.com/ggml-org":       false,
		"https://notwedgy.example/x":        false, // suffix must be on a dot boundary
		"https://example.com/wedgy.example": false, // path, not host
		"not a url at all":                  false,
	}
	for u, want := range cases {
		if got := skipFullRender(u); got != want {
			t.Errorf("skipFullRender(%q) = %v, want %v", u, got, want)
		}
	}
}

func TestWedgeHostDefaultListIsEmpty(t *testing.T) {
	os.Unsetenv("MYMCP_WEDGE_HOSTS")
	for _, u := range []string{
		"https://huggingface.co/Myric/x",
		"https://stackoverflow.com/questions",
		"https://modelscope.cn",
	} {
		if skipFullRender(u) {
			t.Errorf("%s is skipping full render; --use-gl=swiftshader fixed these, "+
				"so a re-added entry is hiding a local browser defect", u)
		}
	}
}

func TestWedgeHostOverride(t *testing.T) {
	t.Setenv("MYMCP_WEDGE_HOSTS", "example.com, foo.test")
	if !skipFullRender("https://example.com/a") {
		t.Error("override host not honoured")
	}
	if !skipFullRender("https://foo.test/a") {
		t.Error("second override host not honoured (whitespace trimming)")
	}
	if skipFullRender("https://other.invalid/a") {
		t.Error("non-listed host should not match")
	}
	os.Setenv("MYMCP_WEDGE_HOSTS", "")
	if skipFullRender("https://example.com/a") {
		t.Error("empty override should disable the list entirely")
	}
}
