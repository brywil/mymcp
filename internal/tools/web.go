package tools

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const maxBodyBytes = 512 << 10 // 512 KiB response cap

// webTools provides outbound HTTP fetches.
type webTools struct{ timeout time.Duration }

func (wt *webTools) register(r *Registry) {
	r.Register(&Tool{
		Name:        "http_fetch",
		Description: "Perform an HTTP request and return status, headers, and (truncated) body.",
		Schema: obj(map[string]interface{}{
			"url":    strProp("Absolute URL"),
			"method": strProp("HTTP method (default GET)"),
			"body":   strProp("Optional request body"),
		}, "url"),
		// Not marked read-only: an HTTP request can have side effects, so it is
		// excluded from the "ro" preset and must be granted explicitly.
		Handler: wt.fetch,
	})
}

func (wt *webTools) fetch(ctx context.Context, a map[string]interface{}) (string, error) {
	url := argString(a, "url")
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return "", fmt.Errorf("url must be absolute http(s)")
	}
	method := strings.ToUpper(argString(a, "method"))
	if method == "" {
		method = "GET"
	}
	ctx, cancel := context.WithTimeout(ctx, wt.timeout)
	defer cancel()

	var body io.Reader
	if b := argString(a, "body"); b != "" {
		body = strings.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", resp.Status)
	fmt.Fprintf(&b, "Content-Type: %s\n\n", resp.Header.Get("Content-Type"))
	b.Write(data)
	if resp.ContentLength > maxBodyBytes || len(data) == maxBodyBytes {
		b.WriteString("\n...(truncated)")
	}
	return b.String(), nil
}
