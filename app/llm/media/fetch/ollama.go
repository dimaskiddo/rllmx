package fetch

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
)

// Ollama reads a page through Ollama Cloud's web fetch, which shares the
// provider's key.
type Ollama struct{}

// Kind is the service it serves.
func (Ollama) Kind() string { return upstream.KindFetch }

// Build makes the fetch call.
func (Ollama) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	raw, err := json.Marshal(map[string]any{"url": target(r)})
	if err != nil {
		return media.Call{}, media.BadRequestf("the request cannot be built")
	}
	h := media.Headers(r, nil)
	h["Content-Type"] = "application/json"
	return media.Call{Method: http.MethodPost, URL: address(r, "https://ollama.com/api/web_fetch"), Header: h, Body: raw}, nil
}

// Parse reads what Ollama extracted.
func (Ollama) Parse(_ context.Context, r media.Request, _ media.Client, status int, _ http.Header, raw []byte) (media.Response, error) {
	if status < 200 || status > 299 {
		return media.Response{}, refused(status)
	}
	content := gjson.GetBytes(raw, "content")
	if content.Type != gjson.String {
		return media.Response{}, &media.ProviderError{Status: http.StatusBadGateway, Text: "the provider answered no page"}
	}
	links := []string{}
	for _, v := range gjson.GetBytes(raw, "links").Array() {
		if v.Type == gjson.String && len(links) < 256 {
			links = append(links, v.String())
		}
	}
	return reply("ollama", target(r), format(r), gjson.GetBytes(raw, "title").String(), content.String(), links, limit(r), cost(r))
}
