package search

import (
	"cmp"
	"context"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
)

// Serper answers Google's index through its own API.
type Serper struct{}

// Kind is the service it serves.
func (Serper) Kind() string { return upstream.KindSearch }

// Build makes the search call.
func (Serper) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	path := "/search"
	if field(r, "search_type") == "news" {
		path = "/news"
	}
	payload := map[string]any{"q": field(r, "query"), "num": count(r, "max_results")}
	if v := field(r, "country"); v != "" {
		payload["gl"] = strings.ToLower(v)
	}
	if v := field(r, "language"); v != "" {
		payload["hl"] = v
	}
	return call(http.MethodPost, base(r, "https://google.serper.dev")+path, headers(r, nil), payload)
}

// Parse reads what Serper answered.
func (Serper) Parse(_ context.Context, r media.Request, _ media.Client, status int, _ http.Header, raw []byte) (media.Response, error) {
	if status < 200 || status > 299 {
		return media.Response{}, refused(status)
	}
	items := gjson.GetBytes(raw, "organic")
	if field(r, "search_type") == "news" {
		items = gjson.GetBytes(raw, "news")
	}
	limit := count(r, "max_results")
	out := []result{}
	for i, v := range items.Array() {
		if i >= limit {
			break
		}
		out = append(out, item("serper", i+1, v.Get("title").String(), v.Get("link").String()).
			withSnippet(cmp.Or(v.Get("snippet").String(), v.Get("description").String())).
			withPublished(v.Get("date").String()))
	}
	var total *int
	if t := gjson.GetBytes(raw, "searchParameters.totalResults"); t.Type == gjson.Number {
		n := int(t.Int())
		total = &n
	}
	return reply("serper", field(r, "query"), out, nil, usage{QueriesUsed: 1, SearchCost: cost(r)}, total, nil)
}
