package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// Preflight exercises the browser stack one layer at a time and names the layer that
// broke, plus the fix. It exists because every browser failure this project has hit was
// SILENT and surfaced far from its cause:
//
//	missing --remote-allow-origins  -> "missing or bad WebSocket-Origin", reads as a client bug
//	profile under a hidden dir      -> snap cannot write it; unit "exited during startup"
//	no software GL backend          -> captureScreenshot hangs FOREVER, pages never complete
//	server still loading            -> HTTP 200-shaped 503; curl exits 0 and looks healthy
//
// Each of those cost hours to identify. A stage that names itself costs seconds.
//
// The page probes use a data: URL, never a real site, so a network problem can never be
// misreported as a browser problem. Stages run in dependency order and stop at the first
// failure: once the websocket is refused, "screenshot" tells you nothing.

type PreflightStage struct {
	Name   string
	OK     bool
	Detail string
	Fix    string
	Took   time.Duration
}

const probePage = "data:text/html,<html><body style='background:%23c0ffee'><h1>mymcp preflight</h1></body></html>"

// Preflight runs the checks against endpoint (e.g. http://127.0.0.1:9222).
// A stage that fails cold is retried once: snap-confined Chromium is genuinely flaky on a
// cold start (measured: 3 of 4 fresh launches hung at Page.navigate on flags that then ran
// 15/15 warm), so a single failure is not evidence of misconfiguration.
func Preflight(ctx context.Context, endpoint string) []PreflightStage {
	endpoint = strings.TrimSuffix(endpoint, "/")
	var out []PreflightStage

	add := func(s PreflightStage) bool {
		out = append(out, s)
		return s.OK
	}

	// 1. Is anything listening, and is it a browser?
	t0 := time.Now()
	ver, err := probeVersion(endpoint)
	if err != nil {
		add(PreflightStage{Name: "reachable", Detail: err.Error(), Took: time.Since(t0),
			Fix: browserAbsentFix(endpoint)})
		return out
	}
	add(PreflightStage{Name: "reachable", OK: true, Detail: ver, Took: time.Since(t0)})

	// 2-4. Page-level stages, retried once as a whole on failure.
	var stages []PreflightStage
	for attempt := 0; attempt < 2; attempt++ {
		stages = probePage2(ctx, endpoint)
		if allOK(stages) {
			if attempt > 0 {
				stages = append(stages, PreflightStage{Name: "note", OK: true,
					Detail: "passed only on the second attempt -- normal for a cold browser, " +
						"but if it recurs on a warm one, something is genuinely wrong"})
			}
			break
		}
		if attempt == 0 {
			time.Sleep(2 * time.Second)
		}
	}
	out = append(out, stages...)
	return out
}

func allOK(s []PreflightStage) bool {
	for _, x := range s {
		if !x.OK {
			return false
		}
	}
	return len(s) > 0
}

func probeVersion(endpoint string) (string, error) {
	c := &http.Client{Timeout: 5 * time.Second}
	resp, err := c.Get(endpoint + "/json/version")
	if err != nil {
		return "", fmt.Errorf("nothing accepting connections at %s (%v)", endpoint, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s answered HTTP %d, not a DevTools endpoint", endpoint, resp.StatusCode)
	}
	var v map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return "", fmt.Errorf("%s answered, but not with DevTools JSON (%v)", endpoint, err)
	}
	b, _ := v["Browser"].(string)
	if b == "" {
		return "", fmt.Errorf("%s returned no Browser field", endpoint)
	}
	return b, nil
}

func probePage2(ctx context.Context, endpoint string) []PreflightStage {
	var out []PreflightStage
	mgr := &browserMgr{cdpURL: endpoint}

	t0 := time.Now()
	wsURL, id, err := mgr.newTab()
	if err != nil {
		return append(out, PreflightStage{Name: "new-tab", Detail: err.Error(), Took: time.Since(t0),
			Fix: "The DevTools HTTP endpoint works but will not open a tab. Usually the browser is\n" +
				"    mid-shutdown or out of memory. Restart it: systemctl --user restart goclaw-chromium"})
	}
	defer mgr.closeTab(id)
	out = append(out, PreflightStage{Name: "new-tab", OK: true, Detail: "page target created", Took: time.Since(t0)})

	t0 = time.Now()
	conn, err := dialCDP(wsURL)
	if err != nil {
		return append(out, PreflightStage{Name: "websocket", Detail: err.Error(), Took: time.Since(t0),
			Fix: "The browser is refusing the DevTools websocket upgrade. It must be launched with\n" +
				"    --remote-allow-origins=*   (without it x/net/websocket cannot connect at all,\n" +
				"    and the error reads like a client bug). Add it to goclaw-chromium-start.sh."})
	}
	defer conn.close()
	out = append(out, PreflightStage{Name: "websocket", OK: true, Detail: "DevTools upgrade accepted", Took: time.Since(t0)})

	pctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, err := conn.call(pctx, "Page.enable", nil); err != nil {
		return append(out, PreflightStage{Name: "page-enable", Detail: err.Error(),
			Fix: "Page domain refused. Restart the browser."})
	}

	t0 = time.Now()
	if _, err := conn.call(pctx, "Page.navigate", map[string]interface{}{"url": probePage}); err != nil {
		return append(out, PreflightStage{Name: "navigate", Detail: err.Error(), Took: time.Since(t0),
			Fix: "Navigation to a data: URL failed or hung -- no network is involved, so this is the\n" +
				"    browser itself. Most likely a cold/contended browser; restart it and retry:\n" +
				"    systemctl --user restart goclaw-chromium"})
	}
	out = append(out, PreflightStage{Name: "navigate", OK: true, Detail: "data: URL loaded", Took: time.Since(t0)})
	time.Sleep(600 * time.Millisecond)

	t0 = time.Now()
	ectx, ecancel := context.WithTimeout(ctx, 15*time.Second)
	defer ecancel()
	r, err := conn.call(ectx, "Runtime.evaluate", map[string]interface{}{
		"expression": "document.readyState", "returnByValue": true})
	if err != nil {
		return append(out, PreflightStage{Name: "evaluate", Detail: err.Error(), Took: time.Since(t0),
			Fix: "Runtime.evaluate hung. The renderer cannot finish a page. On aarch64 this is the\n" +
				"    signature of a headless browser with no software GL backend: add\n" +
				"    --use-gl=swiftshader to the launch flags. (It also makes screenshots hang.)"})
	}
	state := digString(r, "result", "value")
	out = append(out, PreflightStage{Name: "evaluate", OK: true, Detail: "readyState=" + state, Took: time.Since(t0)})

	t0 = time.Now()
	sctx, scancel := context.WithTimeout(ctx, 20*time.Second)
	defer scancel()
	sr, err := conn.call(sctx, "Page.captureScreenshot", map[string]interface{}{"format": "png"})
	if err != nil {
		return append(out, PreflightStage{Name: "screenshot", Detail: err.Error(), Took: time.Since(t0),
			Fix: "captureScreenshot hung or failed while DOM access works. The renderer cannot\n" +
				"    composite a frame. Add --use-gl=swiftshader to the launch flags:\n" +
				"    --headless=new --disable-gpu on its own leaves no path to a painted frame.\n" +
				"    Text browsing (web_inspect, web_search_free) still works without this;\n" +
				"    only screenshots and vision do not."})
	}
	data := digString(sr, "data")
	if len(data) < 100 {
		return append(out, PreflightStage{Name: "screenshot", Detail: "returned an empty image", Took: time.Since(t0),
			Fix: "Screenshot returned but carried no data. Restart the browser and retry."})
	}
	out = append(out, PreflightStage{Name: "screenshot", OK: true,
		Detail: fmt.Sprintf("%d bytes of base64 PNG", len(data)), Took: time.Since(t0)})
	return out
}

func digString(m map[string]interface{}, keys ...string) string {
	var cur interface{} = m
	for _, k := range keys {
		mm, ok := cur.(map[string]interface{})
		if !ok {
			return ""
		}
		cur = mm[k]
	}
	if s, ok := cur.(string); ok {
		return s
	}
	// A nil map satisfies the map[string]interface{} assertion above, so a missing key walks
	// to an untyped nil rather than bailing out. Without this, "%v" renders it as "<nil>" and
	// that string ends up in a user-facing detail line.
	if cur == nil {
		return ""
	}
	return fmt.Sprintf("%v", cur)
}

// browserAbsentFix distinguishes "no browser installed" from "installed but not running",
// because the remedies are completely different and the symptom is identical.
func browserAbsentFix(endpoint string) string {
	var found []string
	for _, c := range []string{"chromium", "chromium-browser", "google-chrome-stable", "google-chrome"} {
		if p, err := exec.LookPath(c); err == nil {
			if runnable(p) {
				found = append(found, p)
			} else {
				found = append(found, p+" (on PATH but will not run -- Ubuntu ships chromium-browser as a snap stub)")
			}
		}
	}
	b := &strings.Builder{}
	fmt.Fprintf(b, "Nothing is serving the DevTools protocol at %s.\n", endpoint)
	if len(found) == 0 {
		b.WriteString("    No browser found on PATH. Install one:\n" +
			"      Ubuntu/Debian with snap:  sudo snap install chromium\n" +
			"      without snap (e.g. snapd masked):  install Google Chrome's .deb\n" +
			"    Then set GOCLAW_CHROMIUM_BIN if it is not named 'chromium'.")
		return b.String()
	}
	b.WriteString("    A browser IS installed:\n")
	for _, f := range found {
		fmt.Fprintf(b, "      %s\n", f)
	}
	b.WriteString("    So it is not running, not that it is missing. Start it:\n" +
		"      systemctl --user start goclaw-chromium\n" +
		"      systemctl --user status goclaw-chromium   # if it will not stay up\n" +
		"    Set MYMCP_CDP_URL if the browser listens somewhere other than the default.")
	return b.String()
}

func runnable(path string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, path, "--version").Run() == nil
}

// FormatPreflight renders stages for a terminal or a tool reply.
func FormatPreflight(stages []PreflightStage) (string, bool) {
	b := &strings.Builder{}
	ok := true
	for _, s := range stages {
		mark := "ok  "
		if !s.OK {
			mark = "FAIL"
			ok = false
		}
		took := ""
		if s.Took > 0 {
			took = fmt.Sprintf(" (%.2fs)", s.Took.Seconds())
		}
		fmt.Fprintf(b, "  [%s] %-11s %s%s\n", mark, s.Name, s.Detail, took)
		if !s.OK && s.Fix != "" {
			fmt.Fprintf(b, "    %s\n", s.Fix)
		}
	}
	if ok {
		b.WriteString("\n  Browser stack is healthy: DOM access and screenshots both work.\n")
	} else {
		b.WriteString("\n  Browser stack is NOT fully working. Stages run in dependency order;\n" +
			"  fix the first FAIL, then re-run -- later stages cannot be trusted until it passes.\n")
	}
	return b.String(), ok
}

var browserHealthSchema = map[string]interface{}{
	"type":       "object",
	"properties": map[string]interface{}{},
}

func browserHealth(ctx context.Context, _ map[string]interface{}) (string, error) {
	ep := (&browserMgr{cdpURL: defaultCDPURL}).endpoint()
	report, ok := FormatPreflight(Preflight(ctx, ep))
	status := "HEALTHY"
	if !ok {
		status = "DEGRADED"
	}
	return fmt.Sprintf("browser stack: %s (endpoint %s)\n\n%s", status, ep, report), nil
}

func registerBrowserHealth(r *Registry) {
	r.Register(&Tool{
		Name: "browser_health",
		Description: "Check the headless browser the web tools depend on, one layer at a time " +
			"(reachable, websocket, navigate, evaluate, screenshot). Run this when web_inspect, " +
			"web_search_free, or a screenshot fails, to find out WHICH layer is broken and how to " +
			"fix it, instead of guessing.",
		Schema:   browserHealthSchema,
		ReadOnly: true,
		Handler:  browserHealth,
	})
}
