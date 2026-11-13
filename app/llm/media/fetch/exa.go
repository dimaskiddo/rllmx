package fetch

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
)

// Exa reads a page through Exa's contents API.
type Exa struct{}

// Kind is the service it serves.
func (Exa) Kind() string { return upstream.KindFetch }

// Build makes the contents call.
func (Exa) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	raw, err := json.Marshal(map[string]any{"ids": []string{target(r)}, "text": true})
	if err != nil {
		return media.Call{}, media.BadRequestf("the request cannot be built")
	}
	h := media.Headers(r, nil)
	h["Content-Type"] = "application/json"
	return media.Call{Method: http.MethodPost, URL: address(r, "https://api.exa.ai/contents"), Header: h, Body: raw}, nil
}

// Parse reads what Exa extracted.
func (Exa) Parse(_ context.Context, r media.Request, _ media.Client, status int, _ http.Header, raw []byte) (media.Response, error) {
	if status < 200 || status > 299 {
		return media.Response{}, refused(status)
	}
	first := gjson.GetBytes(raw, "results.0")
	return reply("exa", target(r), format(r), first.Get("title").String(), first.Get("text").String(), nil, limit(r), cost(r))
}
