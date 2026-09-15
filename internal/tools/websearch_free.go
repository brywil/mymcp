package tools

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

var webSearchFreeSchema = map[string]interface{}{
	"type": "object",
	"properties": map[string]interface{}{
		"query": map[string]interface{}{
			"type":        "string",
			"description": "What to search for.",
		},
		"count": map[string]interface{}{
			"type":        "integer",
			"description": "How many results to return (default 8, max 20).",
		},
	},
	"required": []string{"query"},
}

// braveExtract pulls result title/url/snippet out of a rendered Brave results page.
//
// Brave's class names are hashed per deploy (svelte-jmfu5f and friends), so this keys
// off data-type=web and href shape only -- the two things that survive a redeploy.
// The .snippet class is deliberately NOT used: it also matches Brave's own AI answer
// box, which is not a search result.
const braveExtract = `(() => {
  const out = [];
  const seen = new Set();
  for (const el of document.querySelectorAll('[data-type=web]')) {
    const a = el.querySelector('a[href^="http"]');
    if (!a) continue;
    let host;
    try { host = new URL(a.href).hostname; } catch (e) { continue; }
    if (/(^|\.)brave\.com$/.test(host)) continue;
    if (seen.has(a.href)) continue;
    seen.add(a.href);
    let title = ((el.querySelector('[class*=title]') || a).innerText || '').trim().split('\n')[0];
    let snip = '';
    for (const d of el.querySelectorAll('div,p')) {
      const t = (d.innerText || '').trim();
      if (t.length > snip.length && t.length < 600 && !t.includes(title.slice(0, 20))) snip = t;
    }
    out.push({
      title: title.slice(0, 200),
      url: a.href,
      snippet: snip.replace(/\s+/g, ' ').slice(0, 300),
    });
  }
  return out;
})()`

func webSearchFree(ctx context.Context, args map[string]interface{}) (string, error) {
	query, _ := args["query"].(string)
	query = strings.TrimSpace(query)
	if query == "" {
		return "", fmt.Errorf("query is required")
	}
	count := 8
	if n, ok := args["count"].(float64); ok && n > 0 {
		count = int(n)
	}
	if count > 20 {
		count = 20
	}

	target := "https://search.brave.com/search?q=" + url.QueryEscape(query)
	val, err := evalOnPageMode(ctx, target, braveExtract, 3500, false)
	if err != nil {
		return "", fmt.Errorf("browser search failed: %w (is the goclaw-chromium service running?)", err)
	}

	rows, _ := val.([]interface{})
	if len(rows) == 0 {
		return fmt.Sprintf("No results extracted for %q. Brave may have served a consent "+
			"or challenge page. Try web_inspect on %s to see what came back.", query, target), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d result(s) for %q (source: Brave Search, no API cost)\n\n", min(len(rows), count), query)
	for i, r := range rows {
		if i >= count {
			break
		}
		m, ok := r.(map[string]interface{})
		if !ok {
			continue
		}
		title, _ := m["title"].(string)
		u, _ := m["url"].(string)
		snip, _ := m["snippet"].(string)
		if title == "" {
			title = u
		}
		fmt.Fprintf(&b, "%d. %s\n   %s\n", i+1, title, u)
		if snip != "" {
			fmt.Fprintf(&b, "   %s\n", snip)
		}
		b.WriteString("\n")
	}
	return guardExternal(b.String()), nil
}

func registerWebSearchFree(r *Registry) {
	r.Register(&Tool{
		Name: "web_search_free",
		Description: "Search the web at NO COST, by driving the local headless browser against " +
			"Brave Search. Prefer this over web_search_paid for ordinary lookups -- it is free and " +
			"returns titles, URLs and snippets. Follow a result with web_inspect to read the page. " +
			"Use web_search_paid only when this fails or when you need its richer page content.",
		Schema:   webSearchFreeSchema,
		ReadOnly: true,
		Handler:  webSearchFree,
	})
}
