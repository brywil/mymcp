package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The failure this reproduces happened in the wild. Asked to read a Hugging Face model card,
// web_fetch called Ollama's hosted fetch API, got HTTP 404 {"error": "not found"}, and had no
// other way to reach the page -- even though a plain GET of the same URL returns it in full,
// and this package already ships an http_request tool that would have worked.
//
// The caller (an LLM) received the error, searched eight more times, and then INVENTED the
// document's contents. The model was told plainly and confabulated anyway, so the fix is not
// better error text: it is not failing in the first place when the content is one GET away.
func TestDirectFetchReturnsMarkdownVerbatim(t *testing.T) {
	const doc = "# Real Title\n\n| model | tokens |\n|---|---|\n| Muse | -35.8% |\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		_, _ = w.Write([]byte(doc))
	}))
	defer srv.Close()

	ws := &webSearchTools{}
	got, err := ws.directFetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("directFetch: %v", err)
	}
	// VERBATIM matters: HTML-stripping a Markdown document would mangle the tables and
	// numbers that are the entire reason someone fetches a model card.
	if got != doc {
		t.Errorf("markdown was altered.\n got: %q\nwant: %q", got, doc)
	}
}

// HTML gets the parser, and script/style content must not come through -- a modern page is
// mostly minified JavaScript, which is worse than returning nothing because it looks like
// content and burns the caller's context.
func TestDirectFetchStripsScriptsFromHTML(t *testing.T) {
	html := `<html><head><title>T</title><style>body{color:red}</style></head>` +
		`<body><script>var secret="MINIFIED_JS_NOISE";</script>` +
		`<h1>Measured Result</h1><p>tokens dropped 35.8 percent</p></body></html>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(html))
	}))
	defer srv.Close()

	ws := &webSearchTools{}
	got, err := ws.directFetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("directFetch: %v", err)
	}
	if !strings.Contains(got, "Measured Result") || !strings.Contains(got, "35.8") {
		t.Errorf("lost the visible text: %q", got)
	}
	if strings.Contains(got, "MINIFIED_JS_NOISE") || strings.Contains(got, "color:red") {
		t.Errorf("script/style leaked into the text: %q", got)
	}
}

// An empty 200 is a failure, not a document. Returning "" as success is how a fallback
// hands back a convincing blank -- the exact shape of bug this whole change is about.
func TestDirectFetchRejectsEmptyBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ws := &webSearchTools{}
	if _, err := ws.directFetch(context.Background(), srv.URL); err == nil {
		t.Fatal("an empty 200 must be an error, not an empty success")
	}
}

func TestDirectFetchReportsNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	ws := &webSearchTools{}
	_, err := ws.directFetch(context.Background(), srv.URL)
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("want the status code in the error, got %v", err)
	}
}

// Several hosts serve a stub or refuse outright to unfamiliar user agents, which would turn
// the fallback into a second confusing failure instead of a rescue.
func TestDirectFetchSendsABrowserUserAgent(t *testing.T) {
	var ua string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	ws := &webSearchTools{}
	if _, err := ws.directFetch(context.Background(), srv.URL); err != nil {
		t.Fatalf("directFetch: %v", err)
	}
	if !strings.Contains(ua, "Mozilla") {
		t.Errorf("unfamiliar user agent %q invites a stub response", ua)
	}
}
