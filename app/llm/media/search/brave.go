package search

import (
	"cmp"
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
)

// Brave searches Brave's own index.
type Brave struct{}

// Kind is the service it serves.
func (Brave) Kind() string { return upstream.KindSearch }

// Build makes the search call.
func (Brave) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	path := "/web/search"
	if field(r, "search_type") == "news" {
		path = "/news/search"
	}
	q := url.Values{"q": {field(r, "query")}, "count": {strconv.Itoa(count(r, "max_results"))}}
	if v := field(r, "country"); v != "" {
		q.Set("country", v)
	}
	if v := field(r, "language"); v != "" {
		q.Set("search_lang", v)
	}
	return call(http.MethodGet, base(r, "https://api.search.brave.com/res/v1")+path+"?"+q.Encode(), headers(r, nil), nil)
}

// Parse reads what Brave answered.
func (Brave) Parse(_ context.Context, r media.Request, _ media.Client, status int, _ http.Header, raw []byte) (media.Response, error) {
	if status < 200 || status > 299 {
		return media.Response{}, refused(status)
	}
	container := gjson.GetBytes(raw, "web")
	if field(r, "search_type") == "news" {
		container = gjson.GetBytes(raw, "news")
		if !container.Exists() {
			container = gjson.ParseBytes(raw)
		}
	}
	items := container.Get("results")
	limit := count(r, "max_results")
	out := []result{}
	for i, v := range items.Array() {
		if i >= limit {
			break
		}
		out = append(out, item("brave-search", i+1, v.Get("title").String(), v.Get("url").String()).
			withSnippet(v.Get("description").String()).
			withPublished(cmp.Or(v.Get("page_age").String(), v.Get("age").String())).
			withFavicon(cmp.Or(v.Get("meta_url.favicon").String(), v.Get("favicon").String())))
	}
	var total *int
	if t := container.Get("totalCount"); t.Type == gjson.Number {
		n := int(t.Int())
		total = &n
	}
	return reply("brave-search", field(r, "query"), out, nil, usage{QueriesUsed: 1, SearchCost: cost(r)}, total, nil)
}
