package search

import (
	"context"
	"net/http"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
)

// Tavily searches Tavily's index.
type Tavily struct{}

// Kind is the service it serves.
func (Tavily) Kind() string { return upstream.KindSearch }

// Build makes the search call.
func (Tavily) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	includes, excludes := domainFilter(r)
	topic := "general"
	if field(r, "search_type") == "news" {
		topic = "news"
	}
	payload := map[string]any{"query": field(r, "query"), "max_results": count(r, "max_results"), "topic": topic}
	if len(includes) > 0 {
		payload["include_domains"] = includes
	}
	if len(excludes) > 0 {
		payload["exclude_domains"] = excludes
	}
	if v := field(r, "country"); v != "" {
		payload["country"] = v
	}
	return call(http.MethodPost, base(r, "https://api.tavily.com/search"), headers(r, nil), payload)
}

// Parse reads what Tavily answered.
func (Tavily) Parse(_ context.Context, r media.Request, _ media.Client, status int, _ http.Header, raw []byte) (media.Response, error) {
	if status < 200 || status > 299 {
		return media.Response{}, refused(status)
	}
	limit := count(r, "max_results")
	out := []result{}
	for i, v := range gjson.GetBytes(raw, "results").Array() {
		if i >= limit {
			break
		}
		out = append(out, item("tavily", i+1, v.Get("title").String(), v.Get("url").String()).
			withSnippet(v.Get("content").String()).
			withScore(v.Get("score").Float()).
			withPublished(v.Get("published_date").String()).
			withContent("text", v.Get("raw_content").String()))
	}
	total := len(out)
	return reply("tavily", field(r, "query"), out, nil, usage{QueriesUsed: 1, SearchCost: cost(r)}, &total, nil)
}
