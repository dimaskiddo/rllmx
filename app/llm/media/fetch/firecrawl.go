package fetch

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
)

// Firecrawl reads a page through Firecrawl's scrape API.
type Firecrawl struct{}

// Kind is the service it serves.
func (Firecrawl) Kind() string { return upstream.KindFetch }

// Build makes the scrape call.
func (Firecrawl) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	raw, err := json.Marshal(map[string]any{"url": target(r), "formats": []string{format(r)}})
	if err != nil {
		return media.Call{}, media.BadRequestf("the request cannot be built")
	}
	h := media.Headers(r, nil)
	h["Content-Type"] = "application/json"
	return media.Call{Method: http.MethodPost, URL: address(r, "https://api.firecrawl.dev/v1/scrape"), Header: h, Body: raw}, nil
}

// Parse reads what Firecrawl extracted.
func (Firecrawl) Parse(_ context.Context, r media.Request, _ media.Client, status int, _ http.Header, raw []byte) (media.Response, error) {
	if status < 200 || status > 299 {
		return media.Response{}, refused(status)
	}
	data := gjson.GetBytes(raw, "data")
	text := firstOf(data.Get("markdown").String(), data.Get("html").String(), data.Get("text").String())
	return reply("firecrawl", target(r), format(r), data.Get("metadata.title").String(), text, nil, limit(r), cost(r))
}

// firstOf is the first non-empty value.
func firstOf(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
