package tools

import (
	"os"
	"testing"
)

func TestWedgeHostMatching(t *testing.T) {
	cases := map[string]bool{
		"https://huggingface.co/Myric/x":     true,
		"https://HUGGINGFACE.CO/a":           true, // case-insensitive
		"https://cdn.huggingface.co/f.gguf":  true, // subdomain
		"https://github.com/ggml-org":        false,
		"https://nothuggingface.co/x":        false, // suffix must be on a dot boundary
		"https://example.com/huggingface.co": false, // path, not host
		"not a url at all":                   false,
	}
	for u, want := range cases {
		if got := skipFullRender(u); got != want {
			t.Errorf("skipFullRender(%q) = %v, want %v", u, got, want)
		}
	}
}

func TestWedgeHostOverride(t *testing.T) {
	t.Setenv("MYMCP_WEDGE_HOSTS", "example.com, foo.test")
	if !skipFullRender("https://example.com/a") {
		t.Error("override host not honoured")
	}
	if skipFullRender("https://huggingface.co/a") {
		t.Error("override should REPLACE the built-in list, not extend it")
	}
	os.Setenv("MYMCP_WEDGE_HOSTS", "")
	if skipFullRender("https://huggingface.co/a") {
		t.Error("empty override should disable the list entirely")
	}
}
