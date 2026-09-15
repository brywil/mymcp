package tools

// An INSPECTABLE DOM for text-only models.
//
// web_fetch goes through Ollama's hosted fetch API, which returns whatever the server sent --
// no JavaScript runs, so a modern app-shell page comes back as an empty skeleton, and the model
// has no way to look at one part of a page without re-reading all of it.
//
// This drives a real headless Chromium over the DevTools Protocol instead: the page executes,
// and the model gets a STRUCTURED view it can navigate -- an outline first, then a CSS selector
// to drill into whatever looked interesting. That is the difference between reading a page and
// being able to inspect it.
//
// Deliberately stateless per call (navigate fresh each time) but backed by a REUSED browser
// process, so repeated calls cost ~100ms instead of a 1s cold start. A screenshot mode is
// intentionally absent: Page.captureScreenshot hangs on Chromium 152 (snap rev 3527) on this
// box, while every other CDP command is fine. Rendering lands when that is resolved -- see
// docs/BROWSER.md.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/net/websocket"
)

const (
	// The dedicated headless Chromium already managed by the goclaw-chromium user unit.
	// We CONNECT; we do not launch. Running our own browser meant two processes competing for
	// ports and snap profiles, and a second thing to fix every time the snap updates
	// underneath us -- which is what made this fail repeatedly.
	defaultCDPURL = "http://127.0.0.1:9222"
	navSettleMS   = 1200
	defaultMaxLen = 12000
)

type browserMgr struct {
	cdpURL   string
	llamaURL string // only for deriving chat-template markers; see sanitize.go
}

var theBrowser = &browserMgr{cdpURL: defaultCDPURL}

// endpoint returns the CDP HTTP base, honouring MYMCP_CDP_URL for a non-default port.
func (b *browserMgr) endpoint() string {
	if v := os.Getenv("MYMCP_CDP_URL"); v != "" {
		return strings.TrimSuffix(v, "/")
	}
	if b.cdpURL == "" {
		return defaultCDPURL
	}
	return strings.TrimSuffix(b.cdpURL, "/")
}

// version reports what is ACTUALLY rendering. Read from CDP, never from `chromium --version`:
// a long-lived process keeps its snap revision mounted after an update, so the launcher and the
// running browser routinely disagree.
func (b *browserMgr) version() (string, error) {
	c := &http.Client{Timeout: 4 * time.Second}
	resp, err := c.Get(b.endpoint() + "/json/version")
	if err != nil {
		return "", fmt.Errorf("no headless Chromium at %s -- is goclaw-chromium running? (%w)", b.endpoint(), err)
	}
	defer resp.Body.Close()
	var v map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return "", err
	}
	s, _ := v["Browser"].(string)
	return s, nil
}

type cdpTarget struct {
	Type  string `json:"type"`
	WSURL string `json:"webSocketDebuggerUrl"`
	URL   string `json:"url"`
	Title string `json:"title"`
	ID    string `json:"id"`
}

// pageTarget returns a websocket URL for a PAGE target, creating one if none exists.
// Attaching to a non-page target (extension background pages and browser_ui targets are both
// present in a fresh headless browser) makes Page.* commands hang with no error.
func (b *browserMgr) pageTarget() (string, error) {
	c := &http.Client{Timeout: 5 * time.Second}
	resp, err := c.Get(b.endpoint() + "/json")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var ts []cdpTarget
	if err := json.NewDecoder(resp.Body).Decode(&ts); err != nil {
		return "", err
	}
	for _, t := range ts {
		if t.Type == "page" && t.WSURL != "" {
			return t.WSURL, nil
		}
	}
	req, _ := http.NewRequest("PUT", b.endpoint()+"/json/new?about:blank", nil)
	r2, err := c.Do(req)
	if err != nil {
		return "", fmt.Errorf("no page target and could not create one: %w", err)
	}
	defer r2.Body.Close()
	var t cdpTarget
	if err := json.NewDecoder(r2.Body).Decode(&t); err != nil || t.WSURL == "" {
		return "", fmt.Errorf("could not create a page target")
	}
	return t.WSURL, nil
}

type cdpConn struct {
	ws *websocket.Conn
	id int
}

func dialCDP(wsURL string) (*cdpConn, error) {
	// Chromium refuses a DevTools websocket upgrade from an Origin it was not told to allow, and
	// x/net/websocket refuses to dial WITHOUT one -- so the browser must be started with
	// --remote-allow-origins. goclaw-chromium passes it; the failure otherwise is
	// "missing or bad WebSocket-Origin", which reads like a client bug and is not one.
	ws, err := websocket.Dial(wsURL, "", "http://127.0.0.1/")
	if err != nil {
		return nil, fmt.Errorf("cdp dial %s: %w", wsURL, err)
	}
	return &cdpConn{ws: ws}, nil
}

func (c *cdpConn) close() { _ = c.ws.Close() }

// call sends one CDP command and waits for the matching reply, ignoring unrelated events.
func (c *cdpConn) call(ctx context.Context, method string, params map[string]interface{}) (map[string]interface{}, error) {
	c.id++
	id := c.id
	if params == nil {
		params = map[string]interface{}{}
	}
	if err := websocket.JSON.Send(c.ws, map[string]interface{}{"id": id, "method": method, "params": params}); err != nil {
		return nil, fmt.Errorf("%s: send: %w", method, err)
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(30 * time.Second)
	}
	for {
		if err := c.ws.SetReadDeadline(deadline); err != nil {
			return nil, err
		}
		var msg map[string]interface{}
		if err := websocket.JSON.Receive(c.ws, &msg); err != nil {
			return nil, fmt.Errorf("%s: %w", method, err)
		}
		if f, _ := msg["id"].(float64); int(f) == id {
			if e, ok := msg["error"]; ok {
				return nil, fmt.Errorf("%s: %v", method, e)
			}
			res, _ := msg["result"].(map[string]interface{})
			return res, nil
		}
	}
}

// evalOnPage navigates to url and evaluates js, returning its JSON-serialisable value.
func evalOnPage(ctx context.Context, url, js string, waitMS int) (interface{}, error) {
	if _, err := theBrowser.version(); err != nil {
		return nil, err
	}
	wsURL, err := theBrowser.pageTarget()
	if err != nil {
		return nil, err
	}
	conn, err := dialCDP(wsURL)
	if err != nil {
		return nil, err
	}
	defer conn.close()
	if _, err := conn.call(ctx, "Page.enable", nil); err != nil {
		return nil, err
	}
	if _, err := conn.call(ctx, "Page.navigate", map[string]interface{}{"url": url}); err != nil {
		return nil, err
	}
	if waitMS <= 0 {
		waitMS = navSettleMS
	}
	select {
	case <-time.After(time.Duration(waitMS) * time.Millisecond):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	res, err := conn.call(ctx, "Runtime.evaluate", map[string]interface{}{
		"expression": js, "returnByValue": true, "awaitPromise": true})
	if err != nil {
		return nil, err
	}
	r, _ := res["result"].(map[string]interface{})
	if sub, ok := res["exceptionDetails"]; ok {
		return nil, fmt.Errorf("page script failed: %v", sub)
	}
	return r["value"], nil
}

// ---- extractors. The heavy lifting runs IN the page: one Runtime.evaluate returns a finished
// structure, which is far less brittle than walking the DOM.* CDP domains command by command.

const jsOutline = `(() => {
  const vis = e => { const r = e.getBoundingClientRect();
    const s = getComputedStyle(e);
    return r.width > 0 && r.height > 0 && s.visibility !== 'hidden' && s.display !== 'none'; };
  const txt = e => (e.innerText || e.textContent || '').trim().replace(/\s+/g, ' ');
  const out = { title: document.title, url: location.href, headings: [], landmarks: [],
                links: 0, forms: [], buttons: [] };
  document.querySelectorAll('h1,h2,h3,h4').forEach(h => {
    if (vis(h) && txt(h)) out.headings.push({ level: h.tagName, text: txt(h).slice(0, 120) }); });
  document.querySelectorAll('nav,main,header,footer,aside,[role]').forEach(e => {
    if (!vis(e)) return;
    const role = e.getAttribute('role') || e.tagName.toLowerCase();
    out.landmarks.push({ role, sel: e.id ? '#' + e.id : e.tagName.toLowerCase(),
                         chars: txt(e).length }); });
  out.links = [...document.querySelectorAll('a[href]')].filter(vis).length;
  document.querySelectorAll('form').forEach(f => {
    const fields = [...f.querySelectorAll('input,select,textarea')].map(i =>
      (i.name || i.id || i.type || 'field') + ':' + (i.type || i.tagName.toLowerCase()));
    out.forms.push({ action: f.getAttribute('action') || '', fields: fields.slice(0, 12) }); });
  [...document.querySelectorAll('button,[role=button],input[type=submit]')].filter(vis)
    .slice(0, 25).forEach(b => out.buttons.push(txt(b).slice(0, 60) || '(unlabelled)'));
  // Controls the agent can actually aim at. Label falls back through the same chain
  // dnd-ai-dm uses (aria-label -> text -> value -> placeholder -> id), and each carries the
  // section it lives in, because "Save" in three panels is three different buttons.
  const seen = new Set();
  out.controls = [];
  document.querySelectorAll('button,a[href],summary,input,select,textarea,[role=button]').forEach(el => {
    if (!vis(el)) return;
    let label = (el.getAttribute('aria-label') || el.textContent || el.value ||
                 el.getAttribute('placeholder') || '').replace(/\s+/g,' ').trim();
    if (!label && el.id) label = el.id;
    if (!label) return;
    label = label.slice(0, 60);
    const ref = el.id ? ('#'+el.id) : label;
    const key = ref + '|' + el.tagName;
    if (seen.has(key)) return; seen.add(key);
    let section = '';
    const box = el.closest('form,section,nav,article,main,[role=dialog],.panel,.modal');
    if (box) { const h = box.querySelector('h1,h2,h3,legend,summary');
               if (h) section = (h.textContent||'').replace(/\s+/g,' ').trim().slice(0,40); }
    out.controls.push({ref, label, kind: el.tagName.toLowerCase(), section});
  });
  out.controls = out.controls.slice(0, 60);
  return out; })()`

const jsText = `(() => {
  const drop = ['script','style','noscript','svg','iframe'];
  drop.forEach(t => document.querySelectorAll(t).forEach(e => e.remove()));
  const main = document.querySelector('main,article,[role=main]') || document.body;
  return (main.innerText || '').replace(/\n{3,}/g, '\n\n').trim(); })()`

const jsLinks = `(() => {
  const seen = new Set(); const out = [];
  document.querySelectorAll('a[href]').forEach(a => {
    const r = a.getBoundingClientRect();
    if (!(r.width > 0 && r.height > 0)) return;
    const href = a.href; if (seen.has(href)) return; seen.add(href);
    const t = (a.innerText || a.textContent || '').trim().replace(/\s+/g, ' ');
    out.push({ text: t.slice(0, 90) || '(no text)', href }); });
  return out; })()`

func jsQuery(sel string) string {
	b, _ := json.Marshal(sel)
	return `(() => {
  const els = [...document.querySelectorAll(` + string(b) + `)];
  return { count: els.length, nodes: els.slice(0, 25).map(e => ({
      tag: e.tagName.toLowerCase(),
      id: e.id || '', cls: (e.className && e.className.toString ? e.className.toString() : '').slice(0,80),
      text: (e.innerText || e.textContent || '').trim().replace(/\s+/g,' ').slice(0, 300),
      href: e.getAttribute('href') || '', html: e.outerHTML.slice(0, 400) })) }; })()`
}

// truncateReply is deliberately not web.go's truncate: this one reports how much was withheld,
// so a model can tell "that is the whole page" from "there is more here" and re-query with a
// narrower selector instead of assuming it saw everything.
func truncateReply(s string, n int) string {
	if n <= 0 {
		n = defaultMaxLen
	}
	if len(s) <= n {
		return s
	}
	return s[:n] + fmt.Sprintf("\n... [truncated: %d of %d chars shown -- narrow the selector or raise max_chars]", n, len(s))
}

func (b *browserMgr) inspect(ctx context.Context, args map[string]interface{}) (string, error) {
	url, _ := args["url"].(string)
	if strings.TrimSpace(url) == "" {
		return "", fmt.Errorf("url is required")
	}
	if !strings.Contains(url, "://") {
		url = "https://" + url
	}
	mode, _ := args["mode"].(string)
	if mode == "" {
		mode = "outline"
	}
	waitMS := 0
	if f, ok := args["wait_ms"].(float64); ok {
		waitMS = int(f)
	}
	maxLen := defaultMaxLen
	if f, ok := args["max_chars"].(float64); ok && int(f) > 0 {
		maxLen = int(f)
	}

	var js string
	switch mode {
	case "outline":
		js = jsOutline
	case "text":
		js = jsText
	case "links":
		js = jsLinks
	case "query":
		sel, _ := args["selector"].(string)
		if strings.TrimSpace(sel) == "" {
			return "", fmt.Errorf("mode=query needs a 'selector'")
		}
		js = jsQuery(sel)
	default:
		return "", fmt.Errorf("unknown mode %q (outline|text|links|query)", mode)
	}

	val, err := evalOnPage(ctx, url, js, waitMS)
	if err != nil {
		return "", fmt.Errorf("inspect %s: %w", url, err)
	}
	// EVERYTHING below this line is attacker-controlled. One guarded exit, so a future mode
	// cannot accidentally return raw page text.
	var body string
	switch {
	case mode == "outline":
		if m, ok := val.(map[string]interface{}); ok {
			body = renderScene(m)
		}
	case true:
		if s, ok := val.(string); ok {
			body = s
		}
	}
	if body == "" {
		pretty, err := json.MarshalIndent(val, "", "  ")
		if err != nil {
			return "", err
		}
		body = string(pretty)
	}
	return truncateReply(guardExternal(body, b.llamaURL), maxLen), nil
}

// renderScene lays a page out the way dnd-ai-dm renders a room (engine/adapter.py `_scene_text`):
// flat labelled sections, entities named rather than described, and detail deliberately withheld
// for a follow-up look. That shape works because the NAMES are the handles for the next action --
// here a CSS selector for mode=query, there an object to examine. A nested JSON dump of the same
// facts costs far more tokens and reads worse.
func renderScene(m map[string]interface{}) string {
	str := func(k string) string { v, _ := m[k].(string); return v }
	arr := func(k string) []interface{} { v, _ := m[k].([]interface{}); return v }

	var b strings.Builder
	title := str("title")
	if title == "" {
		title = "(untitled)"
	}
	b.WriteString(title + "\n" + str("url") + "\n")

	if hs := arr("headings"); len(hs) > 0 {
		var lines []string
		for _, h := range hs {
			hm, _ := h.(map[string]interface{})
			lvl, _ := hm["level"].(string)
			txt, _ := hm["text"].(string)
			lines = append(lines, fmt.Sprintf("%s %s", strings.ToLower(lvl), txt))
		}
		if len(lines) > 30 {
			lines = append(lines[:30], fmt.Sprintf("(+%d more headings)", len(lines)-30))
		}
		b.WriteString("\nOutline:\n  " + strings.Join(lines, "\n  ") + "\n")
	}

	if ls := arr("landmarks"); len(ls) > 0 {
		var parts []string
		for _, l := range ls {
			lm, _ := l.(map[string]interface{})
			role, _ := lm["role"].(string)
			sel, _ := lm["sel"].(string)
			chars, _ := lm["chars"].(float64)
			parts = append(parts, fmt.Sprintf("%s [%s, %d chars]", role, sel, int(chars)))
		}
		if len(parts) > 15 {
			parts = parts[:15]
		}
		b.WriteString("\nRegions: " + strings.Join(parts, ", ") + "\n")
	}

	if bs := arr("buttons"); len(bs) > 0 {
		var parts []string
		for _, x := range bs {
			if t, _ := x.(string); t != "" {
				parts = append(parts, t)
			}
		}
		if len(parts) > 0 {
			b.WriteString("\nActions: " + strings.Join(parts, ", ") + "\n")
		}
	}

	if fs := arr("forms"); len(fs) > 0 {
		for _, f := range fs {
			fm, _ := f.(map[string]interface{})
			act, _ := fm["action"].(string)
			var flds []string
			for _, x := range arr2(fm, "fields") {
				if t, _ := x.(string); t != "" {
					flds = append(flds, t)
				}
			}
			b.WriteString(fmt.Sprintf("\nForm %s: %s\n", act, strings.Join(flds, ", ")))
		}
	}

	if cs := arr("controls"); len(cs) > 0 {
		var lines []string
		for _, c := range cs {
			cm, _ := c.(map[string]interface{})
			ref, _ := cm["ref"].(string)
			label, _ := cm["label"].(string)
			kind, _ := cm["kind"].(string)
			sec, _ := cm["section"].(string)
			line := fmt.Sprintf("%s (%s)", label, kind)
			if sec != "" {
				line += " in " + sec
			}
			if strings.HasPrefix(ref, "#") {
				line += " -> " + ref
			}
			lines = append(lines, line)
		}
		if len(lines) > 40 {
			lines = append(lines[:40], fmt.Sprintf("(+%d more controls)", len(lines)-40))
		}
		b.WriteString("\nControls:\n  " + strings.Join(lines, "\n  ") + "\n")
	}

	if n, ok := m["links"].(float64); ok && n > 0 {
		b.WriteString(fmt.Sprintf("\nExits: %d links (mode=links to list them)\n", int(n)))
	}
	b.WriteString("\nLook closer with mode=query and a CSS selector, or mode=text for the body.")
	return b.String()
}

func arr2(m map[string]interface{}, k string) []interface{} {
	v, _ := m[k].([]interface{})
	return v
}

var webInspectSchema = map[string]interface{}{
	"type": "object",
	"properties": map[string]interface{}{
		"url": map[string]interface{}{"type": "string",
			"description": "Page to load. A real browser renders it, so JavaScript-built pages work."},
		"mode": map[string]interface{}{"type": "string",
			"enum": []string{"outline", "text", "links", "query"},
			"description": "outline: structure first (title, headings, landmarks, forms, buttons) -- start here. " +
				"text: readable body text. links: visible links with their text. " +
				"query: drill into a CSS selector."},
		"selector":  map[string]interface{}{"type": "string", "description": "CSS selector, required for mode=query."},
		"wait_ms":   map[string]interface{}{"type": "integer", "description": "Extra settle time after load for slow apps (default 1200)."},
		"max_chars": map[string]interface{}{"type": "integer", "description": "Truncate the reply (default 12000)."},
	},
	"required": []string{"url"},
}

func registerBrowser(r *Registry, llamaURL string) {
	theBrowser.llamaURL = llamaURL
	r.Register(&Tool{
		Name: "web_inspect",
		Description: "Load a page in a real headless browser and inspect its DOM. Unlike web_fetch " +
			"(which reads raw server HTML and cannot run JavaScript), this executes the page, so " +
			"app-shell sites work. Use mode=outline first to see the structure, then mode=query with " +
			"a CSS selector to read just the part you need, instead of pulling the whole page.",
		Schema:   webInspectSchema,
		ReadOnly: true,
		Handler:  theBrowser.inspect,
	})
}
