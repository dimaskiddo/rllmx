package fetch

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
)

// TinyFish reads a page through TinyFish's fetch API.
type TinyFish struct{}

// Kind is the service it serves.
func (TinyFish) Kind() string { return upstream.KindFetch }

// Build makes the fetch call.
func (TinyFish) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	raw, err := json.Marshal(map[string]any{"urls": []string{target(r)}, "format": format(r)})
	if err != nil {
		return media.Call{}, media.BadRequestf("the request cannot be built")
	}
	h := media.Headers(r, nil)
	h["Content-Type"] = "application/json"
	return media.Call{Method: http.MethodPost, URL: address(r, "https://api.fetch.tinyfish.ai"), Header: h, Body: raw}, nil
}

// Parse reads what TinyFish extracted, refusing a page it could not read.
func (TinyFish) Parse(_ context.Context, r media.Request, _ media.Client, status int, _ http.Header, raw []byte) (media.Response, error) {
	if status < 200 || status > 299 {
		return media.Response{}, refused(status)
	}
	if failure := gjson.GetBytes(raw, "errors.0"); failure.Exists() {
		return media.Response{}, &media.ProviderError{Status: http.StatusBadGateway, Text: "the provider could not read the page"}
	}
	page := gjson.GetBytes(raw, "results.0")
	if page.Get("text").Type != gjson.String {
		return media.Response{}, &media.ProviderError{Status: http.StatusBadGateway, Text: "the provider answered no page"}
	}
	links := []string{}
	for _, v := range page.Get("links").Array() {
		if v.Type == gjson.String && len(links) < 256 {
			links = append(links, v.String())
		}
	}
	out, err := reply("tinyfish", target(r), format(r), page.Get("title").String(), page.Get("text").String(), links, limit(r), cost(r))
	return out, err
}
