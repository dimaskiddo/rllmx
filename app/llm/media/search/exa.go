package search

import (
	"cmp"
	"context"
	"net/http"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
	apptext "rllmx/app/platform/text"
)

// Exa searches Exa's neural index.
type Exa struct{}

// Kind is the service it serves.
func (Exa) Kind() string { return upstream.KindSearch }

// Build makes the search call.
func (Exa) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	includes, excludes := domainFilter(r)
	payload := map[string]any{
		"query": field(r, "query"), "numResults": count(r, "max_results"),
		"type": "auto", "text": true, "highlights": true,
	}
	if len(includes) > 0 {
		payload["includeDomains"] = includes
	}
	if len(excludes) > 0 {
		payload["excludeDomains"] = excludes
	}
	if field(r, "search_type") == "news" {
		payload["category"] = "news"
	}
	return call(http.MethodPost, base(r, "https://api.exa.ai/search"), headers(r, nil), payload)
}

// Parse reads what Exa answered.
func (Exa) Parse(_ context.Context, r media.Request, _ media.Client, status int, _ http.Header, raw []byte) (media.Response, error) {
	if status < 200 || status > 299 {
		return media.Response{}, refused(status)
	}
	limit := count(r, "max_results")
	out := []result{}
	for i, v := range gjson.GetBytes(raw, "results").Array() {
		if i >= limit {
			break
		}
		text := v.Get("text").String()
		snippet := cmp.Or(v.Get("highlights.0").String(), apptext.CutRunes(text, 300))
		out = append(out, item("exa", i+1, v.Get("title").String(), v.Get("url").String()).
			withSnippet(snippet).
			withScore(v.Get("score").Float()).
			withPublished(v.Get("publishedDate").String()).
			withFavicon(v.Get("favicon").String()).
			withAuthor(v.Get("author").String()).
			withImage(v.Get("image").String()).
			withContent("text", text))
	}
	total := len(out)
	return reply("exa", field(r, "query"), out, nil, usage{QueriesUsed: 1, SearchCost: cost(r)}, &total, nil)
}
