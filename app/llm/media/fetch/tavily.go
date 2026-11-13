package fetch

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
)

// Tavily reads a page through Tavily's extract API.
type Tavily struct{}

// Kind is the service it serves.
func (Tavily) Kind() string { return upstream.KindFetch }

// Build makes the extract call.
func (Tavily) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	raw, err := json.Marshal(map[string]any{"urls": []string{target(r)}, "extract_depth": "basic", "format": format(r)})
	if err != nil {
		return media.Call{}, media.BadRequestf("the request cannot be built")
	}
	h := media.Headers(r, nil)
	h["Content-Type"] = "application/json"
	return media.Call{Method: http.MethodPost, URL: address(r, "https://api.tavily.com/extract"), Header: h, Body: raw}, nil
}

// Parse reads what Tavily extracted.
func (Tavily) Parse(_ context.Context, r media.Request, _ media.Client, status int, _ http.Header, raw []byte) (media.Response, error) {
	if status < 200 || status > 299 {
		return media.Response{}, refused(status)
	}
	first := gjson.GetBytes(raw, "results.0")
	return reply("tavily", target(r), format(r), "", first.Get("raw_content").String(), nil, limit(r), cost(r))
}
