package fetch

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
)

// Jina reads a page through Jina's reader, which answers markdown or text.
type Jina struct{}

// Kind is the service it serves.
func (Jina) Kind() string { return upstream.KindFetch }

// Build makes the reader call.
func (Jina) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	raw, err := json.Marshal(map[string]any{"url": target(r)})
	if err != nil {
		return media.Call{}, media.BadRequestf("the request cannot be built")
	}
	h := media.Headers(r, nil)
	h["Content-Type"] = "application/json"
	h["x-respond-with"] = format(r)
	return media.Call{Method: http.MethodPost, URL: address(r, "https://r.jina.ai") + "/", Header: h, Body: raw}, nil
}

// Parse reads the page Jina returned.
func (Jina) Parse(_ context.Context, r media.Request, _ media.Client, status int, _ http.Header, raw []byte) (media.Response, error) {
	if status < 200 || status > 299 {
		return media.Response{}, refused(status)
	}
	text := string(raw)
	return reply("jina-reader", target(r), format(r), jinaTitle(text), text, nil, limit(r), cost(r))
}

// jinaTitle is the page's title in the metadata Jina prepends, else its first
// heading.
var (
	jinaTitleLine = regexp.MustCompile(`(?mi)^\s*Title:\s*(.+)$`)
	jinaHeading   = regexp.MustCompile(`(?m)^\s*#\s+(.+)$`)
)

func jinaTitle(text string) string {
	if m := jinaTitleLine.FindStringSubmatch(text); m != nil {
		return strings.TrimSpace(m[1])
	}
	if m := jinaHeading.FindStringSubmatch(text); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}
