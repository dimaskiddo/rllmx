package search

import (
	"cmp"
	"context"
	"net/http"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
)

// OllamaSearch uses Ollama Cloud's web search, which shares the provider's key.
type OllamaSearch struct{}

// Kind is the service it serves.
func (OllamaSearch) Kind() string { return upstream.KindSearch }

// Build makes the search call.
func (OllamaSearch) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	payload := map[string]any{"query": field(r, "query"), "max_results": count(r, "max_results")}
	if v := field(r, "country"); v != "" {
		payload["country"] = v
	}
	if v := field(r, "language"); v != "" {
		payload["language"] = v
	}
	return call(http.MethodPost, base(r, "https://ollama.com/api/web_search"), headers(r, nil), payload)
}

// Parse reads what Ollama answered.
func (OllamaSearch) Parse(_ context.Context, r media.Request, _ media.Client, status int, _ http.Header, raw []byte) (media.Response, error) {
	if status < 200 || status > 299 {
		return media.Response{}, refused(status)
	}
	items := gjson.GetBytes(raw, "results")
	if !items.Exists() {
		items = gjson.ParseBytes(raw)
	}
	out := []result{}
	for i, v := range items.Array() {
		if i >= count(r, "max_results") {
			break
		}
		text := v.Get("content").String()
		out = append(out, item("ollama-search", i+1, v.Get("title").String(), v.Get("url").String()).
			withSnippet(cmp.Or(text, v.Get("snippet").String())).
			withContent("text", text).
			withPublished(v.Get("published_at").String()).
			withSource(v.Get("source").String()))
	}
	total := len(out)
	return reply("ollama-search", field(r, "query"), out, nil, usage{QueriesUsed: 1, SearchCost: free()}, &total, nil)
}
