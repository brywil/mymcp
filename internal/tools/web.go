package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	nethtml "golang.org/x/net/html"
)

// =============================================================================
// httpTools: raw HTTP client + content parsers (http_request, parse_html,
// parse_json, parse_css). Ported from goclaw internal/tools/http_tool.go.
// =============================================================================

// httpTools provides a raw HTTP client and HTML/JSON/CSS parsers.
type httpTools struct {
	timeout      time.Duration // request timeout (goclaw default 30s)
	allowedHosts []string      // if non-nil, restricts requests to these hosts
	client       *http.Client
}

func (ht *httpTools) register(r *Registry) {
	if ht.client == nil {
		ht.client = &http.Client{
			Timeout: ht.timeout,
			Transport: &http.Transport{
				MaxIdleConns:        10,
				MaxIdleConnsPerHost: 5,
				IdleConnTimeout:     90 * time.Second,
			},
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	// http_request is NOT read-only: it can POST/PUT/DELETE.
	r.Register(&Tool{
		Name:        "http_request",
		Description: "Send a raw HTTP request (any method, headers, body) and return status, headers, and body.",
		Schema:      httpRequestSchema,
		ReadOnly:    false,
		Handler:     ht.httpRequest,
	})
	r.Register(&Tool{
		Name:        "parse_html",
		Description: "Fetch or accept HTML and extract text, headings, links, meta, or structure.",
		Schema:      parseHTMLSchema,
		ReadOnly:    true,
		Handler:     ht.parseHTML,
	})
	r.Register(&Tool{
		Name:        "parse_json",
		Description: "Fetch or accept JSON, describe its structure, format it, or extract dot-notation paths.",
		Schema:      parseJSONSchema,
		ReadOnly:    true,
		Handler:     ht.parseJSON,
	})
	r.Register(&Tool{
		Name:        "parse_css",
		Description: "Fetch or accept HTML and extract elements matching a CSS selector.",
		Schema:      parseCSSSchema,
		ReadOnly:    true,
		Handler:     ht.parseCSS,
	})
}

var httpRequestSchema = map[string]interface{}{
	"type": "object",
	"properties": map[string]interface{}{
		"url": map[string]interface{}{
			"type":        "string",
			"description": "The URL to send the request to (http:// or https://)",
		},
		"method": map[string]interface{}{
			"type":        "string",
			"description": "HTTP method: GET, POST, PUT, DELETE, PATCH, HEAD, OPTIONS. Default: GET",
		},
		"body": map[string]interface{}{
			"type":        "string",
			"description": "Request body (for POST, PUT, PATCH). JSON-encoded if content_type is application/json.",
		},
		"headers": map[string]interface{}{
			"type":        "object",
			"description": "Additional HTTP headers as key-value pairs.",
			"additionalProperties": map[string]interface{}{
				"type": "string",
			},
		},
		"content_type": map[string]interface{}{
			"type":        "string",
			"description": "Content-Type header value. Common values: application/json, text/html, text/plain, multipart/form-data. If omitted, inferred from body.",
		},
		"follow_redirects": map[string]interface{}{
			"type":        "boolean",
			"description": "Whether to follow redirects. Default: false (returns the redirect response itself).",
		},
		"max_response_size": map[string]interface{}{
			"type":        "integer",
			"description": "Maximum response body size in bytes to read. Default: 1048576 (1MB). Set to 0 for unlimited.",
		},
	},
	"required": []string{"url"},
}

func (ht *httpTools) httpRequest(ctx context.Context, args map[string]interface{}) (string, error) {
	rawURL, ok := args["url"].(string)
	if !ok || rawURL == "" {
		return "", fmt.Errorf("url is required and must be a string")
	}

	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("invalid URL: %s", err.Error())
	}

	if len(ht.allowedHosts) > 0 {
		host := parsedURL.Hostname()
		allowed := false
		for _, a := range ht.allowedHosts {
			if a == "*" || host == a || strings.HasSuffix(host, "."+a) {
				allowed = true
				break
			}
		}
		if !allowed {
			return "", fmt.Errorf("host %s is not in the allowed list: %v", host, ht.allowedHosts)
		}
	}

	method, _ := args["method"].(string)
	if method == "" {
		method = "GET"
	}
	method = strings.ToUpper(method)

	var bodyReader io.Reader
	if body, ok := args["body"].(string); ok && body != "" {
		bodyReader = strings.NewReader(body)
	}

	contentType, _ := args["content_type"].(string)
	if contentType == "" && bodyReader != nil {
		bodyStr, _ := args["body"].(string)
		trimmed := strings.TrimSpace(bodyStr)
		if len(trimmed) > 0 && trimmed[0] == '{' {
			contentType = "application/json"
		}
	}

	maxSize := int64(1048576) // 1MB default
	if maxArg, ok := args["max_response_size"].(float64); ok {
		maxSize = int64(maxArg)
		if maxSize < 0 {
			maxSize = 1048576
		}
	}

	followRedirects := false
	if followArg, ok := args["follow_redirects"].(bool); ok {
		followRedirects = followArg
	}

	req, err := http.NewRequestWithContext(ctx, method, rawURL, bodyReader)
	if err != nil {
		return "", fmt.Errorf("creating request: %s", err.Error())
	}

	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("User-Agent", "goclaw-http-tool/1.0")

	if headers, ok := args["headers"].(map[string]interface{}); ok {
		for kStr, v := range headers {
			if vs, ok := v.(string); ok {
				req.Header.Set(kStr, vs)
			}
		}
	}

	if followRedirects {
		ht.client.CheckRedirect = nil
	} else {
		ht.client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		}
	}

	startTime := time.Now()
	resp, err := ht.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %s", err.Error())
	}
	defer resp.Body.Close()

	duration := time.Since(startTime)

	var bodyBytes []byte
	if maxSize > 0 {
		bodyBytes, err = io.ReadAll(io.LimitReader(resp.Body, maxSize))
	} else {
		bodyBytes, err = io.ReadAll(resp.Body)
	}
	if err != nil {
		return "", fmt.Errorf("reading response body: %s", err.Error())
	}

	var sb strings.Builder
	sb.WriteString("=== HTTP Response ===\n")
	sb.WriteString(fmt.Sprintf("URL: %s\n", rawURL))
	sb.WriteString(fmt.Sprintf("Method: %s\n", method))
	sb.WriteString(fmt.Sprintf("Status: %d %s\n", resp.StatusCode, resp.Status))
	sb.WriteString(fmt.Sprintf("Duration: %v\n", duration.Round(time.Millisecond)))
	sb.WriteString(fmt.Sprintf("Size: %d bytes\n", len(bodyBytes)))

	sb.WriteString("\n--- Headers ---\n")
	for key, values := range resp.Header {
		for _, v := range values {
			sb.WriteString(fmt.Sprintf("%s: %s\n", key, v))
		}
	}

	sb.WriteString("\n--- Body ---\n")
	bodyStr := string(bodyBytes)
	if len(bodyStr) > 0 {
		sb.WriteString(bodyStr)
		if len(bodyStr) >= int(maxSize) && maxSize > 0 {
			sb.WriteString("\n\n... (truncated, response exceeded max_response_size)")
		}
	} else {
		sb.WriteString("(empty)")
	}

	return sb.String(), nil
}

var parseHTMLSchema = map[string]interface{}{
	"type": "object",
	"properties": map[string]interface{}{
		"url": map[string]interface{}{
			"type":        "string",
			"description": "The URL of the HTML page to parse",
		},
		"input": map[string]interface{}{
			"type":        "string",
			"description": "Raw HTML content to parse (alternative to url — provides HTML directly)",
		},
		"mode": map[string]interface{}{
			"type":        "string",
			"description": "Parsing mode: 'text' (extract visible text only), 'headings' (extract heading levels and text), 'links' (extract all links with text and URLs), 'meta' (extract meta tags: title, description, og tags), 'structure' (extract tag structure with indentation). Default: text",
		},
		"selectors": map[string]interface{}{
			"type":        "array",
			"description": "CSS-like selectors to extract specific elements. Format: tag#id, .class, tag.class, tag[attr=val]. Only works with mode='text' or mode='structure'.",
			"items": map[string]interface{}{
				"type": "string",
			},
		},
	},
}

func (ht *httpTools) parseHTML(ctx context.Context, args map[string]interface{}) (string, error) {
	var htmlStr string

	if u, ok := args["url"].(string); ok && u != "" {
		resp, err := ht.client.Get(u)
		if err != nil {
			return "", fmt.Errorf("fetching URL: %s", err.Error())
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("HTTP %d for %s", resp.StatusCode, u)
		}
		bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 5*1024*1024))
		if err != nil {
			return "", fmt.Errorf("reading response: %s", err.Error())
		}
		htmlStr = string(bodyBytes)
	} else if input, ok := args["input"].(string); ok && input != "" {
		htmlStr = input
	} else {
		return "", fmt.Errorf("either 'url' or 'input' is required")
	}

	doc, err := nethtml.Parse(strings.NewReader(htmlStr))
	if err != nil {
		return "", fmt.Errorf("parsing HTML: %s", err.Error())
	}

	mode, _ := args["mode"].(string)
	if mode == "" {
		mode = "text"
	}

	switch mode {
	case "text":
		return ht.extractText(doc, args["selectors"])
	case "headings":
		return ht.extractHeadings(doc), nil
	case "links":
		return ht.extractLinks(doc), nil
	case "meta":
		return ht.extractMeta(doc), nil
	case "structure":
		return ht.extractStructure(doc, args["selectors"])
	default:
		return "", fmt.Errorf("unknown mode '%s'. Valid modes: text, headings, links, meta, structure", mode)
	}
}

func (ht *httpTools) extractText(doc *nethtml.Node, selectorsArg interface{}) (string, error) {
	var sb strings.Builder
	var lastWasSpace bool

	var walk func(*nethtml.Node)
	walk = func(n *nethtml.Node) {
		if n.Type == nethtml.TextNode {
			text := strings.TrimSpace(n.Data)
			if text != "" {
				if isIgnoredNode(n) {
					return
				}
				if lastWasSpace {
					sb.WriteString(" ")
				}
				sb.WriteString(text)
				lastWasSpace = true
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}

	if selectors, ok := selectorsArg.([]interface{}); ok && len(selectors) > 0 {
		selectorStrs := make([]string, 0, len(selectors))
		for _, s := range selectors {
			if ss, ok := s.(string); ok {
				selectorStrs = append(selectorStrs, ss)
			}
		}
		matchingNodes, err := ht.findNodesBySelector(doc, selectorStrs)
		if err != nil {
			return "", err
		}
		for _, node := range matchingNodes {
			var walkNodeFn func(*nethtml.Node)
			walkNodeFn = func(n *nethtml.Node) {
				if n.Type == nethtml.TextNode {
					text := strings.TrimSpace(n.Data)
					if text != "" && !isIgnoredNode(n) {
						if lastWasSpace {
							sb.WriteString("\n")
						}
						sb.WriteString(text)
						lastWasSpace = true
					}
				}
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					walkNodeFn(c)
				}
			}
			walkNodeFn(node)
		}
	} else {
		walk(doc)
	}

	return strings.TrimSpace(sb.String()), nil
}

func isIgnoredNode(n *nethtml.Node) bool {
	current := n
	for current != nil {
		if current.Type == nethtml.ElementNode {
			tag := strings.ToLower(current.Data)
			if tag == "script" || tag == "style" || tag == "nav" || tag == "footer" || tag == "header" || tag == "aside" {
				return true
			}
		}
		current = current.Parent
	}
	return false
}

func (ht *httpTools) extractHeadings(doc *nethtml.Node) string {
	var sb strings.Builder
	var walk func(*nethtml.Node, int)
	walk = func(n *nethtml.Node, depth int) {
		if n.Type == nethtml.ElementNode {
			tag := strings.ToLower(n.Data)
			if strings.HasPrefix(tag, "h") && len(tag) == 2 {
				level := int(tag[1] - '0')
				if level >= 1 && level <= 6 {
					var text strings.Builder
					for c := n.FirstChild; c != nil; c = c.NextSibling {
						if c.Type == nethtml.TextNode {
							text.WriteString(strings.TrimSpace(c.Data))
						}
					}
					if text.Len() > 0 {
						sb.WriteString(fmt.Sprintf("%s %d. %s\n", strings.Repeat("#", level), level, text.String()))
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c, depth+1)
		}
	}
	walk(doc, 0)
	return strings.TrimSpace(sb.String())
}

func (ht *httpTools) extractLinks(doc *nethtml.Node) string {
	var sb strings.Builder
	var count int
	var walk func(*nethtml.Node)
	walk = func(n *nethtml.Node) {
		if n.Type == nethtml.ElementNode && strings.ToLower(n.Data) == "a" {
			var href, text string
			for _, attr := range n.Attr {
				if attr.Key == "href" {
					href = attr.Val
				}
				if attr.Key == "text" || attr.Key == "title" {
					text = attr.Val
				}
			}
			if text == "" {
				var textBuilder strings.Builder
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					if c.Type == nethtml.TextNode {
						textBuilder.WriteString(strings.TrimSpace(c.Data))
					}
				}
				text = textBuilder.String()
			}
			if href != "" {
				count++
				sb.WriteString(fmt.Sprintf("%d. [%s](%s)\n", count, text, href))
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	if count == 0 {
		return "No links found."
	}
	return fmt.Sprintf("Found %d links:\n%s", count, sb.String())
}

func (ht *httpTools) extractMeta(doc *nethtml.Node) string {
	var sb strings.Builder
	var title strings.Builder

	var walk func(*nethtml.Node)
	walk = func(n *nethtml.Node) {
		if n.Type == nethtml.ElementNode {
			tag := strings.ToLower(n.Data)
			if tag == "title" {
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					if c.Type == nethtml.TextNode {
						title.WriteString(strings.TrimSpace(c.Data))
					}
				}
			}
			if tag == "meta" {
				name, content := "", ""
				for _, attr := range n.Attr {
					lowerKey := strings.ToLower(attr.Key)
					if lowerKey == "name" || lowerKey == "property" {
						name = attr.Val
					}
					if lowerKey == "content" {
						content = attr.Val
					}
				}
				if name != "" && content != "" {
					sb.WriteString(fmt.Sprintf("%s: %s\n", name, content))
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	sb.WriteString(fmt.Sprintf("Title: %s\n", title.String()))
	return strings.TrimSpace(sb.String())
}

func (ht *httpTools) extractStructure(doc *nethtml.Node, selectorsArg interface{}) (string, error) {
	var sb strings.Builder
	var indent int

	if selectors, ok := selectorsArg.([]interface{}); ok && len(selectors) > 0 {
		selectorStrs := make([]string, 0, len(selectors))
		for _, s := range selectors {
			if ss, ok := s.(string); ok {
				selectorStrs = append(selectorStrs, ss)
			}
		}
		matchingNodes, err := ht.findNodesBySelector(doc, selectorStrs)
		if err != nil {
			return "", err
		}
		if len(matchingNodes) > 0 {
			for _, node := range matchingNodes {
				indent = 0
				ht.writeStructure(node, &sb, &indent)
			}
			return strings.TrimSpace(sb.String()), nil
		}
	}

	ht.writeStructure(doc, &sb, &indent)
	return strings.TrimSpace(sb.String()), nil
}

func (ht *httpTools) writeStructure(n *nethtml.Node, sb *strings.Builder, indent *int) {
	if n.Type == nethtml.TextNode {
		text := strings.TrimSpace(n.Data)
		if text != "" {
			sb.WriteString(strings.Repeat("  ", *indent) + text + "\n")
		}
		return
	}

	if n.Type == nethtml.ElementNode {
		parts := []string{strings.ToLower(n.Data)}
		for _, attr := range n.Attr {
			if attr.Key != "" {
				parts = append(parts, fmt.Sprintf("%s=%q", attr.Key, attr.Val))
			}
		}
		sb.WriteString(strings.Repeat("  ", *indent) + strings.Join(parts, " ") + "\n")
		*indent++
	}

	for c := n.FirstChild; c != nil; c = c.NextSibling {
		ht.writeStructure(c, sb, indent)
	}

	if n.Type == nethtml.ElementNode {
		*indent--
	}
}

var parseJSONSchema = map[string]interface{}{
	"type": "object",
	"properties": map[string]interface{}{
		"url": map[string]interface{}{
			"type":        "string",
			"description": "The URL of the JSON endpoint to fetch",
		},
		"input": map[string]interface{}{
			"type":        "string",
			"description": "Raw JSON string to parse (alternative to url — provides JSON directly)",
		},
		"indent": map[string]interface{}{
			"type":        "integer",
			"description": "Number of spaces for indentation. Default: 2. Set to 0 for compact output.",
		},
		"keys": map[string]interface{}{
			"type":        "array",
			"description": "Dot-notation key paths to extract specific values. e.g. ['data.items', 'data.items[0].name']. If provided, only extracts those paths.",
			"items": map[string]interface{}{
				"type": "string",
			},
		},
	},
	"required": []string{},
}

func (ht *httpTools) parseJSON(ctx context.Context, args map[string]interface{}) (string, error) {
	var jsonStr string

	if u, ok := args["url"].(string); ok && u != "" {
		resp, err := ht.client.Get(u)
		if err != nil {
			return "", fmt.Errorf("fetching URL: %s", err.Error())
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("HTTP %d for %s", resp.StatusCode, u)
		}
		bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 5*1024*1024))
		if err != nil {
			return "", fmt.Errorf("reading response: %s", err.Error())
		}
		jsonStr = string(bodyBytes)
	} else if input, ok := args["input"].(string); ok && input != "" {
		jsonStr = input
	} else {
		return "", fmt.Errorf("either 'url' or 'input' is required")
	}

	var raw interface{}
	if err := json.Unmarshal([]byte(jsonStr), &raw); err != nil {
		return "", fmt.Errorf("parsing JSON: %s\n\nRaw input (first 500 chars):\n%s", err.Error(), truncate(jsonStr, 500))
	}

	if keysArg, ok := args["keys"].([]interface{}); ok && len(keysArg) > 0 {
		keyPaths := make([]string, 0, len(keysArg))
		for _, k := range keysArg {
			if ks, ok := k.(string); ok {
				keyPaths = append(keyPaths, ks)
			}
		}
		return ht.extractJSONPaths(raw, keyPaths), nil
	}

	indent := 2
	if indentArg, ok := args["indent"].(float64); ok {
		indent = int(indentArg)
		if indent < 0 {
			indent = 2
		}
	}

	var output string
	if indent == 0 {
		output = jsonStr
	} else {
		formatted, err := json.MarshalIndent(raw, "", strings.Repeat(" ", indent))
		if err != nil {
			return "", fmt.Errorf("formatting JSON: %s", err.Error())
		}
		output = string(formatted)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("=== JSON (%d bytes) ===\n", len(jsonStr)))
	sb.WriteString(ht.describeJSON(raw, 0))
	sb.WriteString("\n\n")
	sb.WriteString(output)

	return sb.String(), nil
}

func (ht *httpTools) describeJSON(v interface{}, depth int) string {
	var sb strings.Builder
	indent := strings.Repeat("  ", depth)

	switch val := v.(type) {
	case map[string]interface{}:
		sb.WriteString(fmt.Sprintf("%sObject with %d keys:\n", indent, len(val)))
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		if len(keys) > 10 {
			keys = keys[:10]
		}
		for _, k := range keys {
			sb.WriteString(fmt.Sprintf("%s  - %s: %s\n", indent, k, ht.typeName(val[k])))
		}
		if len(val) > 10 {
			sb.WriteString(fmt.Sprintf("%s  ... and %d more keys\n", indent, len(val)-10))
		}
	case []interface{}:
		sb.WriteString(fmt.Sprintf("%sArray with %d items:\n", indent, len(val)))
		if len(val) > 0 {
			sb.WriteString(fmt.Sprintf("%s  [0]: %s\n", indent, ht.typeName(val[0])))
		}
		if len(val) > 10 {
			sb.WriteString(fmt.Sprintf("%s  ... and %d more items\n", indent, len(val)-10))
		}
		showCount := len(val)
		if showCount > 5 {
			showCount = 5
		}
		for i := 0; i < showCount; i++ {
			sb.WriteString(fmt.Sprintf("%s  [%d]: %s\n", indent, i, ht.describeSimple(val[i])))
		}
	case string:
		display := val
		if len(display) > 100 {
			display = display[:100] + "..."
		}
		sb.WriteString(fmt.Sprintf("%sString: %q\n", indent, display))
	case float64:
		sb.WriteString(fmt.Sprintf("%sNumber: %.2f\n", indent, val))
	case bool:
		sb.WriteString(fmt.Sprintf("%sBoolean: %v\n", indent, val))
	case nil:
		sb.WriteString(fmt.Sprintf("%sNull\n", indent))
	default:
		sb.WriteString(fmt.Sprintf("%s%T: %v\n", indent, val, val))
	}
	return sb.String()
}

func (ht *httpTools) typeName(v interface{}) string {
	switch v.(type) {
	case map[string]interface{}:
		return "object"
	case []interface{}:
		return "array"
	case string:
		return "string"
	case float64:
		return "number"
	case bool:
		return "boolean"
	case nil:
		return "null"
	default:
		return fmt.Sprintf("%T", v)
	}
}

func (ht *httpTools) describeSimple(v interface{}) string {
	switch val := v.(type) {
	case string:
		if len(val) > 80 {
			return fmt.Sprintf("%q...", val[:80])
		}
		return fmt.Sprintf("%q", val)
	case float64:
		return fmt.Sprintf("%.2f", val)
	case bool:
		return fmt.Sprintf("%v", val)
	case nil:
		return "null"
	case map[string]interface{}:
		return fmt.Sprintf("{%d keys}", len(val))
	case []interface{}:
		return fmt.Sprintf("[%d items]", len(val))
	default:
		return fmt.Sprintf("%v", val)
	}
}

func (ht *httpTools) extractJSONPaths(root interface{}, paths []string) string {
	var sb strings.Builder
	sb.WriteString("=== JSON Path Extraction ===\n\n")

	for _, path := range paths {
		val := ht.getJSONPath(root, path)
		if val != nil {
			formatted, err := json.MarshalIndent(val, "", "  ")
			if err != nil {
				sb.WriteString(fmt.Sprintf("%s: (error formatting: %s)\n\n", path, err.Error()))
			} else {
				sb.WriteString(fmt.Sprintf("%s:\n%s\n\n", path, string(formatted)))
			}
		} else {
			sb.WriteString(fmt.Sprintf("%s: (not found)\n\n", path))
		}
	}

	return sb.String()
}

func (ht *httpTools) getJSONPath(v interface{}, path string) interface{} {
	parts := ht.splitPath(path)
	current := v

	for i, part := range parts {
		if current == nil {
			return nil
		}

		switch val := current.(type) {
		case map[string]interface{}:
			bracketIdx := strings.Index(part, "[")
			if bracketIdx > 0 && strings.HasSuffix(part, "]") {
				key := part[:bracketIdx]
				idxStr := part[bracketIdx+1 : len(part)-1]
				var arrIdx int
				fmt.Sscanf(idxStr, "%d", &arrIdx)
				if arrVal, ok := val[key].([]interface{}); ok {
					if arrIdx < 0 || arrIdx >= len(arrVal) {
						return nil
					}
					current = arrVal[arrIdx]
				} else {
					return nil
				}
			} else {
				if next, ok := val[part]; ok {
					current = next
				} else {
					return nil
				}
			}
		case []interface{}:
			idx := strings.Index(part, "[")
			if idx > 0 {
				key := part[:idx]
				idxStr := part[idx+1 : len(part)-1]
				var arrayIdx int
				fmt.Sscanf(idxStr, "%d", &arrayIdx)
				if arrayIdx < 0 || arrayIdx >= len(val) {
					return nil
				}
				if key != "" {
					if arrVal, ok := val[arrayIdx].(map[string]interface{}); ok {
						if next, ok := arrVal[key]; ok {
							current = next
						} else {
							return nil
						}
					} else {
						return nil
					}
				} else {
					current = val[arrayIdx]
				}
			} else {
				var directIdx int
				fmt.Sscanf(part, "%d", &directIdx)
				if directIdx < 0 || directIdx >= len(val) {
					return nil
				}
				current = val[directIdx]
			}
		default:
			return nil
		}

		if i == len(parts)-1 {
			return current
		}
	}

	return current
}

func (ht *httpTools) splitPath(path string) []string {
	var parts []string
	var current strings.Builder

	for i := 0; i < len(path); i++ {
		ch := path[i]
		if ch == '[' {
			end := strings.Index(path[i:], "]")
			if end > 0 {
				current.WriteString(path[i : i+end+1])
				i += end
			} else {
				current.WriteByte(ch)
			}
		} else if ch == '.' {
			if current.Len() > 0 {
				parts = append(parts, current.String())
				current.Reset()
			}
		} else {
			current.WriteByte(ch)
		}
	}
	if current.Len() > 0 {
		parts = append(parts, current.String())
	}
	return parts
}

var parseCSSSchema = map[string]interface{}{
	"type": "object",
	"properties": map[string]interface{}{
		"url": map[string]interface{}{
			"type":        "string",
			"description": "The URL of the HTML page to query",
		},
		"input": map[string]interface{}{
			"type":        "string",
			"description": "Raw HTML content to query (alternative to url)",
		},
		"selector": map[string]interface{}{
			"type":        "string",
			"description": "CSS selector to match elements. Supports: tag, .class, #id, tag.class, tag#id, tag[attr], tag[attr=val], :contains(text), and the combinators ' ' (descendant), '>' (child), '+' (adjacent sibling), '~' (sibling). Pseudo-classes other than :contains and comma-separated lists are rejected with an error rather than silently matching nothing.",
		},
		"attribute": map[string]interface{}{
			"type":        "string",
			"description": "If set, returns only the value of this attribute from matched elements. If omitted, returns the text content.",
		},
		"limit": map[string]interface{}{
			"type":        "integer",
			"description": "Maximum number of results to return. Default: 50.",
		},
	},
	"required": []string{"selector"},
}

func (ht *httpTools) parseCSS(ctx context.Context, args map[string]interface{}) (string, error) {
	var htmlStr string

	if u, ok := args["url"].(string); ok && u != "" {
		resp, err := ht.client.Get(u)
		if err != nil {
			return "", fmt.Errorf("fetching URL: %s", err.Error())
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("HTTP %d for %s", resp.StatusCode, u)
		}
		bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 5*1024*1024))
		if err != nil {
			return "", fmt.Errorf("reading response: %s", err.Error())
		}
		htmlStr = string(bodyBytes)
	} else if input, ok := args["input"].(string); ok && input != "" {
		htmlStr = input
	} else {
		return "", fmt.Errorf("either 'url' or 'input' is required")
	}

	doc, err := nethtml.Parse(strings.NewReader(htmlStr))
	if err != nil {
		return "", fmt.Errorf("parsing HTML: %s", err.Error())
	}

	selector, _ := args["selector"].(string)
	if selector == "" {
		return "", fmt.Errorf("selector is required")
	}

	matchedNodes, err := ht.querySelectorAll(doc, selector)
	if err != nil {
		return "", fmt.Errorf("invalid selector %q: %s", selector, err.Error())
	}

	limit := 50
	if limitArg, ok := args["limit"].(float64); ok {
		limit = int(limitArg)
		if limit < 1 {
			limit = 50
		}
	}

	if len(matchedNodes) > limit {
		matchedNodes = matchedNodes[:limit]
	}

	attribute, _ := args["attribute"].(string)
	var sb strings.Builder

	if len(matchedNodes) == 0 {
		sb.WriteString(fmt.Sprintf("No elements matched selector: %s", selector))
	} else {
		sb.WriteString(fmt.Sprintf("Found %d matching elements (selector: %s):\n\n", len(matchedNodes), selector))
		for i, node := range matchedNodes {
			sb.WriteString(fmt.Sprintf("--- Result %d ---\n", i+1))
			if attribute != "" {
				for _, attr := range node.Attr {
					if attr.Key == attribute {
						sb.WriteString(fmt.Sprintf("%s: %s\n", attribute, attr.Val))
						break
					}
				}
			} else {
				sb.WriteString(ht.nodeText(node))
			}
			sb.WriteString("\n")
		}
	}

	return sb.String(), nil
}

func (ht *httpTools) nodeText(n *nethtml.Node) string {
	var sb strings.Builder
	var walk func(*nethtml.Node)
	walk = func(node *nethtml.Node) {
		if node.Type == nethtml.TextNode {
			sb.WriteString(strings.TrimSpace(node.Data))
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.TrimSpace(sb.String())
}

// --- basic CSS selector matching ---

func (ht *httpTools) findNodesBySelector(root *nethtml.Node, selectors []string) ([]*nethtml.Node, error) {
	pm := parentMap(root)
	allNodes := ht.getAllNodes(root)
	var results []*nethtml.Node

	for _, sel := range selectors {
		compounds, combs, err := tokenizeCSS(sel)
		if err != nil {
			return nil, err
		}
		for _, node := range allNodes {
			if ok, err := ht.matchesSelectorParts(node, compounds, combs, pm); err != nil {
				return nil, err
			} else if ok {
				results = append(results, node)
			}
		}
	}
	return results, nil
}

func (ht *httpTools) querySelectorAll(root *nethtml.Node, selector string) ([]*nethtml.Node, error) {
	compounds, combs, err := tokenizeCSS(selector)
	if err != nil {
		return nil, err
	}
	pm := parentMap(root)
	allNodes := ht.getAllNodes(root)
	var results []*nethtml.Node

	for _, node := range allNodes {
		if ok, err := ht.matchesSelectorParts(node, compounds, combs, pm); err != nil {
			return nil, err
		} else if ok {
			results = append(results, node)
		}
	}
	return results, nil
}

func (ht *httpTools) getAllNodes(root *nethtml.Node) []*nethtml.Node {
	var nodes []*nethtml.Node
	var walk func(*nethtml.Node)
	walk = func(n *nethtml.Node) {
		nodes = append(nodes, n)
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	return nodes
}

// parentMap records each node's parent so combinators can look backwards
// through the tree.
func parentMap(root *nethtml.Node) map[*nethtml.Node]*nethtml.Node {
	pm := map[*nethtml.Node]*nethtml.Node{}
	var walk func(n *nethtml.Node)
	walk = func(n *nethtml.Node) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			pm[c] = n
			walk(c)
		}
	}
	walk(root)
	return pm
}

// scanCompoundEnd returns the end offset of the compound selector starting at
// start: it runs until a top-level (bracket/paren-balanced) combinator or the
// end of the selector.
func scanCompoundEnd(sel string, start int) int {
	depth := 0
	for i := start; i < len(sel); i++ {
		switch sel[i] {
		case '[', '(':
			depth++
		case ']', ')':
			depth--
		case ' ', '\t', '>', '+', '~':
			if depth == 0 {
				return i
			}
		}
	}
	return len(sel)
}

// tokenizeCSS splits a selector into compound parts and the combinators
// between them. combs[i] is the combinator after compounds[i] (linking it to
// compounds[i+1]) and is one of " " (descendant), ">" (child), "+" (adjacent
// sibling), "~" (sibling). len(combs) == len(compounds)-1.
func tokenizeCSS(selector string) (compounds []string, combs []string, err error) {
	sel := strings.TrimSpace(selector)
	if sel == "" {
		return nil, nil, errors.New("selector is empty")
	}
	if strings.ContainsRune(sel, ',') {
		return nil, nil, errors.New("comma-separated selector lists are not supported — run one selector per call")
	}
	i, n := 0, len(sel)
	for i < n {
		for i < n && (sel[i] == ' ' || sel[i] == '\t') {
			i++
		}
		if i >= n {
			break
		}
		start := i
		i = scanCompoundEnd(sel, start)
		compound := strings.TrimSpace(sel[start:i])
		if compound == "" {
			return nil, nil, fmt.Errorf("malformed selector %q (empty compound)", sel)
		}
		compounds = append(compounds, compound)
		for i < n && (sel[i] == ' ' || sel[i] == '\t') {
			i++
		}
		if i >= n {
			break
		}
		switch sel[i] {
		case '>', '+', '~':
			combs = append(combs, string(sel[i]))
			i++
		default:
			combs = append(combs, " ")
		}
	}
	if len(compounds) != len(combs)+1 {
		return nil, nil, fmt.Errorf("malformed selector %q", sel)
	}
	return compounds, combs, nil
}

// matchCompound matches one compound selector (tag, .class, #id, [attr],
// :contains) against a node. Anything beyond that supported set is an error,
// not a silent no-match: "no elements matched" for :nth-child would be read as
// "the page has no such element", which is a wrong answer dressed as a result.
func (ht *httpTools) matchCompound(node *nethtml.Node, compound string) (bool, error) {
	if node == nil || node.Type != nethtml.ElementNode {
		return false, nil
	}

	compound = strings.TrimSpace(compound)
	if compound == "*" {
		return true, nil
	}

	tag := strings.ToLower(node.Data)

	expectedTag := ""
	var classes []string
	var id string
	var attrSelectors []string

	remaining := compound
	for {
		bracketStart := strings.Index(remaining, "[")
		if bracketStart < 0 {
			break
		}
		bracketEnd := strings.Index(remaining, "]")
		if bracketEnd < 0 {
			break
		}
		attrSel := remaining[bracketStart+1 : bracketEnd]
		attrSelectors = append(attrSelectors, attrSel)
		remaining = remaining[:bracketStart] + remaining[bracketEnd+1:]
	}
	remaining = strings.TrimSpace(remaining)

	if idx := strings.Index(remaining, ":"); idx >= 0 {
		rest := remaining[idx:]
		if !strings.HasPrefix(rest, ":contains(") {
			name := rest
			if end := strings.IndexAny(name, "(, "); end > 0 {
				name = name[:end]
			}
			return false, fmt.Errorf("pseudo-class %q is not supported — only :contains(text) is", strings.TrimSpace(name))
		}
		closeIdx := strings.Index(rest, ")")
		if closeIdx < 0 {
			return false, fmt.Errorf("unterminated :contains( in %q", compound)
		}
		containsText := strings.ToLower(rest[10 : closeIdx])
		remaining = strings.TrimSpace(remaining[:idx] + rest[closeIdx+1:])
		nodeText := strings.ToLower(ht.nodeText(node))
		if !strings.Contains(nodeText, containsText) {
			return false, nil
		}
	}

	var tokens []string
	var delimiters []rune
	current := []rune{}
	for _, r := range remaining {
		if r == '.' || r == '#' {
			if len(current) > 0 {
				tokens = append(tokens, string(current))
				current = nil
			}
			delimiters = append(delimiters, r)
		} else {
			current = append(current, r)
		}
	}
	if len(current) > 0 {
		tokens = append(tokens, string(current))
	}

	if len(tokens) > 0 {
		if len(remaining) > 0 && (remaining[0] == '.' || remaining[0] == '#') {
			expectedTag = ""
		} else {
			expectedTag = strings.ToLower(tokens[0])
		}
	}

	tokenStart := 0
	if expectedTag != "" {
		tokenStart = 1
	}
	for i := tokenStart; i < len(tokens); i++ {
		delIdx := i - tokenStart
		if delIdx < len(delimiters) {
			switch delimiters[delIdx] {
			case '.':
				classes = append(classes, tokens[i])
			case '#':
				id = tokens[i]
			}
		}
	}

	if expectedTag != "" && expectedTag != tag {
		return false, nil
	}

	for _, c := range classes {
		hasClass := false
		for _, attr := range node.Attr {
			if attr.Key == "class" {
				for _, cls := range strings.Fields(attr.Val) {
					if cls == c {
						hasClass = true
						break
					}
				}
			}
		}
		if !hasClass {
			return false, nil
		}
	}

	if id != "" {
		hasID := false
		for _, attr := range node.Attr {
			if attr.Key == "id" && attr.Val == id {
				hasID = true
				break
			}
		}
		if !hasID {
			return false, nil
		}
	}

	for _, attrSel := range attrSelectors {
		attrName := attrSel
		attrVal := ""
		hasEqual := false
		if eqIdx := strings.Index(attrSel, "="); eqIdx > 0 {
			attrName = attrSel[:eqIdx]
			attrVal = attrSel[eqIdx+1:]
			hasEqual = true
			if len(attrVal) >= 2 && ((attrVal[0] == '"' && attrVal[len(attrVal)-1] == '"') ||
				(attrVal[0] == '\'' && attrVal[len(attrVal)-1] == '\'')) {
				attrVal = attrVal[1 : len(attrVal)-1]
			}
		}
		hasAttr := false
		for _, attr := range node.Attr {
			if attr.Key == attrName {
				if !hasEqual || attr.Val == attrVal {
					hasAttr = true
					break
				}
			}
		}
		if !hasAttr {
			return false, nil
		}
	}

	return true, nil
}

// matchesSelectorParts matches a full selector: the node against the last
// compound, then each earlier compound according to its combinator. Each step
// must advance from the node matched by the previous step — reading from the
// original (rightmost) node at every step would reduce any chain of 3+
// compounds to its two rightmost compounds, silently matching the wrong set.
func (ht *httpTools) matchesSelectorParts(node *nethtml.Node, compounds []string, combs []string, pm map[*nethtml.Node]*nethtml.Node) (bool, error) {
	if ok, err := ht.matchCompound(node, compounds[len(compounds)-1]); err != nil {
		return false, err
	} else if !ok {
		return false, nil
	}

	cur := node
	for i := len(compounds) - 2; i >= 0; i-- {
		want := compounds[i]
		switch combs[i] {
		case ">":
			cur = pm[cur]
			if cur == nil {
				return false, nil
			}
			if ok, err := ht.matchCompound(cur, want); err != nil {
				return false, err
			} else if !ok {
				return false, nil
			}
		case " ":
			found := false
			for a := pm[cur]; a != nil; a = pm[a] {
				ok, err := ht.matchCompound(a, want)
				if err != nil {
					return false, err
				}
				if ok {
					cur = a
					found = true
					break
				}
			}
			if !found {
				return false, nil
			}
		case "+":
			cur = cur.PrevSibling
			if cur == nil {
				return false, nil
			}
			if ok, err := ht.matchCompound(cur, want); err != nil {
				return false, err
			} else if !ok {
				return false, nil
			}
		case "~":
			found := false
			for sib := cur.PrevSibling; sib != nil; sib = sib.PrevSibling {
				ok, err := ht.matchCompound(sib, want)
				if err != nil {
					return false, err
				}
				if ok {
					cur = sib
					found = true
					break
				}
			}
			if !found {
				return false, nil
			}
		}
	}
	return true, nil
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// =============================================================================
// webSearchTools: Ollama-backed web search + fetch (web_search, web_fetch). Ported from goclaw
// internal/tools/web_search_tool.go. Results are cached under <root>/web_cache.
// =============================================================================

type ollamaWebSearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Content string `json:"content"`
}

type ollamaWebSearchResponse struct {
	Results []ollamaWebSearchResult `json:"results"`
}

type ollamaWebFetchResult struct {
	Title   string   `json:"title"`
	Content string   `json:"content"`
	Links   []string `json:"links"`
}

// webSearchTools provides web search/fetch tools backed by Ollama's API.
type webSearchTools struct {
	root  string // workspace dir; .env fallback for OLLAMA_API_KEY
	cache string // base dir for web_cache; falls back to root
	mu    sync.Mutex
}

func (ws *webSearchTools) register(r *Registry) {
	r.Register(&Tool{
		Name:        "web_search",
		Description: "Search the web via Ollama. Mode 'small' returns titles/URLs only; 'full' adds content snippets; 'raw' returns raw JSON. If the live search is unavailable, the last cached result for the query is served and clearly marked as possibly stale.",
		Schema:      webSearchSchema,
		ReadOnly:    true,
		Handler:     ws.webSearch,
	})
	r.Register(&Tool{
		Name:        "web_fetch",
		Description: "Fetch the full content of a single URL via Ollama's web fetch API.",
		Schema:      webFetchSchema,
		ReadOnly:    true,
		Handler:     ws.webFetch,
	})
}

// readEnvKey reads an env var, falling back to <root>/.env.
func (ws *webSearchTools) readEnvKey(key string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	if ws.root == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(ws.root, ".env"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 && strings.TrimSpace(parts[0]) == key {
			return strings.TrimSpace(parts[1])
		}
	}
	return ""
}

func (ws *webSearchTools) cacheDir() string {
	if ws.cache != "" {
		return filepath.Join(ws.cache, "web_cache")
	}
	if ws.root == "" {
		return ""
	}
	return filepath.Join(ws.root, "web_cache")
}

func (ws *webSearchTools) ensureCacheDir() error {
	dir := ws.cacheDir()
	if dir == "" {
		return nil
	}
	return os.MkdirAll(dir, 0755)
}

func (ws *webSearchTools) cachePath(query string) string {
	slug := strings.ReplaceAll(strings.ToLower(query), " ", "_")
	slug = strings.ReplaceAll(slug, "/", "_")
	if len(slug) > 200 {
		slug = slug[:200]
	}
	return filepath.Join(ws.cacheDir(), fmt.Sprintf("%s.json", slug))
}

func (ws *webSearchTools) fetchCachePath(u string) string {
	slug := strings.ReplaceAll(strings.ToLower(u), " ", "_")
	slug = strings.ReplaceAll(slug, "/", "_")
	if len(slug) > 200 {
		slug = slug[:200]
	}
	return filepath.Join(ws.cacheDir(), fmt.Sprintf("fetch_%s.json", slug))
}

func (ws *webSearchTools) queryOllamaSearch(query string, maxResults int) (*ollamaWebSearchResponse, error) {
	apiKey := ws.readEnvKey("OLLAMA_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("OLLAMA_API_KEY not set — check .env in workspace directory")
	}

	payload := map[string]interface{}{"query": query}
	if maxResults > 0 && maxResults <= 10 {
		payload["max_results"] = maxResults
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	var resp *http.Response
	respBody := []byte{}
	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequest("POST", "https://ollama.com/api/web_search", strings.NewReader(string(body)))
		if err != nil {
			return nil, fmt.Errorf("creating request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+apiKey)

		client := &http.Client{Timeout: 30 * time.Second}
		resp, err = client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("request failed: %w", err)
		}

		respBody, err = io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("reading response: %w", err)
		}

		if resp.StatusCode == http.StatusOK {
			break
		}

		if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			backoff := time.Duration(attempt+1) * time.Second
			log.Printf("[WebSearch] %s retry %d/3: HTTP %d, backing off %v", query, attempt+1, resp.StatusCode, backoff)
			time.Sleep(backoff)
			continue
		}

		log.Printf("[WebSearch] %s: HTTP %d, body=%s", query, resp.StatusCode, string(respBody))
		return nil, fmt.Errorf("ollama API error (HTTP %d, body: %s)", resp.StatusCode, string(respBody))
	}
	var result ollamaWebSearchResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}

	return &result, nil
}

func (ws *webSearchTools) queryOllamaFetch(targetURL string) (*ollamaWebFetchResult, error) {
	apiKey := ws.readEnvKey("OLLAMA_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("OLLAMA_API_KEY not set — check .env in workspace directory")
	}

	payload := map[string]interface{}{"url": targetURL}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	var resp *http.Response
	respBody := []byte{}
	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequest("POST", "https://ollama.com/api/web_fetch", strings.NewReader(string(body)))
		if err != nil {
			return nil, fmt.Errorf("creating request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+apiKey)

		client := &http.Client{Timeout: 30 * time.Second}
		resp, err = client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("request failed: %w", err)
		}

		respBody, err = io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("reading response: %w", err)
		}

		if resp.StatusCode == http.StatusOK {
			break
		}

		if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			backoff := time.Duration(attempt+1) * time.Second
			log.Printf("[WebFetch] %s retry %d/3: HTTP %d, backing off %v", targetURL, attempt+1, resp.StatusCode, backoff)
			time.Sleep(backoff)
			continue
		}

		return nil, fmt.Errorf("ollama fetch API error (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama fetch API error after 3 retries (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	var result ollamaWebFetchResult
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}

	return &result, nil
}

func (ws *webSearchTools) cacheSearch(query string, resp *ollamaWebSearchResponse) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	if err := ws.ensureCacheDir(); err == nil {
		if data, err := json.MarshalIndent(resp, "", "  "); err == nil {
			os.WriteFile(ws.cachePath(query), data, 0644)
		}
	}
}

// readSearchCache loads a previously cached response for the query. The cache
// is a resilience fallback, not a fast path: we only read it when the live
// search failed, and we always tell the caller the data may be stale.
func (ws *webSearchTools) readSearchCache(query string) (*ollamaWebSearchResponse, error) {
	if ws.cacheDir() == "" {
		return nil, os.ErrNotExist
	}
	data, err := os.ReadFile(ws.cachePath(query))
	if err != nil {
		return nil, err
	}
	var resp ollamaWebSearchResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("corrupt cache for %q: %s", query, err.Error())
	}
	return &resp, nil
}

var webSearchSchema = map[string]interface{}{
	"type": "object",
	"properties": map[string]interface{}{
		"query": map[string]interface{}{
			"type":        "string",
			"description": "The search query string",
		},
		"max_results": map[string]interface{}{
			"type":        "integer",
			"description": "Maximum number of results to return (1-10, default 5)",
		},
		"mode": map[string]interface{}{
			"type":        "string",
			"description": "Output mode: 'small' (titles/URLs only), 'full' (titles/URLs/content snippets), 'raw' (raw JSON). Defaults to 'small'.",
			"enum":        searchModes,
		},
	},
	"required": []string{"query"},
}

func (ws *webSearchTools) webSearch(ctx context.Context, args map[string]interface{}) (string, error) {
	query, ok := args["query"].(string)
	if !ok || query == "" {
		return "", fmt.Errorf("query is required and must be a non-empty string")
	}

	maxResults := 5
	if mr, ok := args["max_results"].(float64); ok {
		if mr >= 1 && mr <= 10 {
			maxResults = int(mr)
		}
	}

	// Validate rather than silently falling back. The schema declares an enum, so
	// an unrecognised mode is a caller mistake — and answering mode="Full" with
	// `small` output looks like "these pages have no content" to a model, costing
	// a whole round trip on a slow local backend to discover otherwise.
	mode := "small"
	if m, ok := args["mode"].(string); ok && strings.TrimSpace(m) != "" {
		mode = strings.ToLower(strings.TrimSpace(m))
		if !validSearchMode(mode) {
			return "", fmt.Errorf("unknown mode %q: expected one of %s", m, strings.Join(searchModes, ", "))
		}
	}

	resp, err := ws.queryOllamaSearch(query, maxResults)
	if err != nil {
		// The live search failed (offline, 429/5xx after retries, or the key is
		// unset). A successful search earlier in the session may be on disk —
		// serving it beats a hard failure, provided we say plainly that it is
		// stale and why the fresh one didn't work.
		if cached, cerr := ws.readSearchCache(query); cerr == nil && len(cached.Results) > 0 {
			out, ferr := formatSearchResults(query, cached, mode)
			if ferr != nil {
				return "", ferr
			}
			return fmt.Sprintf("NOTE: live search failed (%s) — the results below are from a previous cached search and may be stale.\n\n%s", err.Error(), out), nil
		}
		return "", fmt.Errorf("search failed: %s", err.Error())
	}

	if len(resp.Results) == 0 {
		return fmt.Sprintf("No results found for: %s", query), nil
	}

	ws.cacheSearch(query, resp)

	return formatSearchResults(query, resp, mode)
}

// searchModes is the single source of truth for the enum: the schema advertises
// it and the handler validates against it, so the two cannot drift apart.
var searchModes = []string{"small", "full", "raw"}

func validSearchMode(mode string) bool {
	for _, m := range searchModes {
		if m == mode {
			return true
		}
	}
	return false
}

// formatSearchResults renders a response in the requested mode. Split out of
// webSearch so the mode dispatch — the part that actually changed when the three
// separate tools were consolidated — can be tested without an Ollama API key or
// a network round trip.
func formatSearchResults(query string, resp *ollamaWebSearchResponse, mode string) (string, error) {
	switch mode {
	case "raw":
		rawJSON, err := json.MarshalIndent(resp, "", "  ")
		if err != nil {
			return "", fmt.Errorf("formatting response: %s", err.Error())
		}
		return string(rawJSON), nil

	case "full":
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("=== Search Results: %s (%d results) ===\n\n", query, len(resp.Results)))
		for i, r := range resp.Results {
			sb.WriteString(fmt.Sprintf("--- %d. %s ---\n", i+1, r.Title))
			sb.WriteString(fmt.Sprintf("URL: %s\n", r.URL))
			sb.WriteString(fmt.Sprintf("Content: %s\n\n", r.Content))
		}
		return sb.String(), nil

	default: // small
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("=== Search Results: %s (%d results) ===\n\n", query, len(resp.Results)))
		for i, r := range resp.Results {
			sb.WriteString(fmt.Sprintf("%d. %s\n   %s\n\n", i+1, r.Title, r.URL))
		}
		sb.WriteString("Titles and URLs only. Use mode='full' to include content snippets.\n")
		return sb.String(), nil
	}
}

var webFetchSchema = map[string]interface{}{
	"type": "object",
	"properties": map[string]interface{}{
		"url": map[string]interface{}{
			"type":        "string",
			"description": "The URL to fetch (must be an absolute URL)",
		},
	},
	"required": []string{"url"},
}

// directFetch reads a URL over plain HTTP, for use when the hosted fetch API cannot.
//
// Content-Type decides the treatment, and getting that wrong is the whole risk here: a raw
// Markdown or JSON URL must be returned VERBATIM, because HTML-stripping it would quietly
// mangle the exact thing the caller asked for. Only text/html goes through the parser.
func (ws *webSearchTools) directFetch(ctx context.Context, targetURL string) (string, error) {
	const maxBody = 2 << 20 // 2 MiB: enough for any document, bounded for a tool result

	req, err := http.NewRequestWithContext(ctx, "GET", targetURL, nil)
	if err != nil {
		return "", fmt.Errorf("building request: %s", err.Error())
	}
	// A browser-ish UA on purpose. Several hosts (Hugging Face among them) serve a stub or
	// refuse outright to unfamiliar agents, which would turn this fallback into a second
	// confusing failure rather than a rescue.
	req.Header.Set("User-Agent",
		"Mozilla/5.0 (X11; Linux aarch64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0 Safari/537.36")
	req.Header.Set("Accept", "text/markdown, text/plain, text/html;q=0.9, */*;q=0.5")

	client := &http.Client{Timeout: 45 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %s", err.Error())
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return "", fmt.Errorf("reading body: %s", err.Error())
	}
	if len(body) == 0 {
		// An empty 200 is a failure, not a document. Saying so is the difference between
		// this fallback helping and it handing back a convincing blank.
		return "", fmt.Errorf("HTTP 200 but empty body")
	}

	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/html") {
		doc, perr := nethtml.Parse(strings.NewReader(string(body)))
		if perr != nil {
			return "", fmt.Errorf("parsing HTML: %s", perr.Error())
		}
		text := strings.TrimSpace(visibleText(doc))
		if text == "" {
			return "", fmt.Errorf("HTML parsed to no visible text (%d bytes of markup)", len(body))
		}
		return text, nil
	}
	return string(body), nil
}

// visibleText walks an HTML tree collecting text a reader would see, skipping the elements
// whose contents are code rather than prose. Without the skip list a modern page returns
// mostly minified JavaScript, which is worse than returning nothing because it looks like
// content and consumes the caller's context.
func visibleText(n *nethtml.Node) string {
	switch n.Type {
	case nethtml.TextNode:
		return n.Data
	case nethtml.ElementNode:
		switch strings.ToLower(n.Data) {
		case "script", "style", "noscript", "template", "svg", "head":
			return ""
		}
	}
	var sb strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if t := visibleText(c); t != "" {
			sb.WriteString(t)
			if !strings.HasSuffix(t, "\n") {
				sb.WriteString(" ")
			}
		}
	}
	return sb.String()
}

func (ws *webSearchTools) webFetch(ctx context.Context, args map[string]interface{}) (string, error) {
	targetURL, ok := args["url"].(string)
	if !ok || targetURL == "" {
		return "", fmt.Errorf("url is required and must be a non-empty string")
	}

	resp, err := ws.queryOllamaFetch(targetURL)
	if err != nil {
		// Ollama's hosted fetch API is not the only way to read a public URL, and it
		// fails on sites it has no index for: huggingface.co model pages return
		// HTTP 404 {"error": "not found"} from it while a plain GET of the same URL
		// returns the full page. Before this fallback, web_fetch had a single point of
		// failure and no recourse -- it reported the 404 and the caller was left with
		// nothing, even though the content was one GET away and this very package
		// already ships an http_request tool that would have succeeded.
		//
		// Observed consequence: asked to read a Hugging Face model card, an agent got
		// the 404, searched eight more times, then INVENTED the document's contents.
		// A fallback would have made that impossible.
		log.Printf("[WebFetch] Ollama API failed for %s (%s) -- falling back to direct GET",
			targetURL, err.Error())
		text, directErr := ws.directFetch(ctx, targetURL)
		if directErr != nil {
			// Report BOTH failures. Reporting only the second would hide that the
			// primary path is down, which is what matters if it stays down.
			log.Printf("[WebFetch] Direct GET also failed for %s: %s", targetURL, directErr.Error())

			// WORDING MATTERS MORE THAN IT LOOKS. The old message ended in the upstream's
			// own text -- {"error": "not found"} -- which reads as "this page does not
			// exist" when it actually meant "the hosted fetch service could not retrieve
			// it". An agent asked to read a page the user had just linked took the former
			// reading, and was left choosing between telling the user their own repo was
			// missing and inventing the contents. It invented them.
			//
			// So the error now says which of those two things happened. Only the direct
			// GET's status is evidence about the page; the hosted API's is evidence about
			// the hosted API.
			if strings.Contains(directErr.Error(), "HTTP 404") {
				return "", fmt.Errorf("%s returned HTTP 404 on a direct request, so the page "+
					"most likely does not exist at that URL (the hosted fetch API also failed: %s)",
					targetURL, err.Error())
			}
			return "", fmt.Errorf("could not retrieve %s -- this is a TOOL failure and is NOT "+
				"evidence the page is missing. Hosted fetch API: %s. Direct GET: %s. The page may "+
				"well exist; retry with the http_request tool, or a raw-content URL (on Hugging "+
				"Face use /resolve/main/<file> rather than /blob/main/<file>). Do not report the "+
				"page as nonexistent, and do not describe its contents, on the strength of this",
				targetURL, err.Error(), directErr.Error())
		}
		log.Printf("[WebFetch] Direct GET recovered %d chars for %s", len(text), targetURL)
		resp = &ollamaWebFetchResult{Title: targetURL + " (direct GET)", Content: text}
	}

	ws.mu.Lock()
	if err := ws.ensureCacheDir(); err == nil {
		if data, err := json.MarshalIndent(resp, "", "  "); err == nil {
			os.WriteFile(ws.fetchCachePath(targetURL), data, 0644)
		}
	}
	ws.mu.Unlock()

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("=== Fetched: %s ===\n\n", resp.Title))
	sb.WriteString(fmt.Sprintf("URL: %s\n\n", targetURL))

	content := resp.Content
	if len(content) > 8000 {
		content = content[:8000] + " ... [truncated]"
	}
	sb.WriteString(content)

	if len(resp.Links) > 0 {
		sb.WriteString(fmt.Sprintf("\n\n--- Links on page (%d) ---\n", len(resp.Links)))
		for _, link := range resp.Links {
			sb.WriteString(fmt.Sprintf("- %s\n", link))
		}
	}

	return sb.String(), nil
}
