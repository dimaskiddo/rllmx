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

// SearchAPI searches Google's index through SearchApi.
type SearchAPI struct{}

// Kind is the service it serves.
func (SearchAPI) Kind() string { return upstream.KindSearch }

// Build makes the search call. The key rides in the address, as its API takes it.
func (SearchAPI) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	if r.Credential == "" {
		return media.Call{}, media.BadRequestf("this provider needs an API key")
	}
	engine := "google"
	if field(r, "search_type") == "news" {
		engine = "google_news"
	}
	q := url.Values{
		"engine": {engine}, "q": {field(r, "query")},
		"api_key": {r.Credential}, "gl": {field(r, "country")},
	}
	if v := field(r, "language"); v != "" {
		q.Set("hl", v)
	}
	if max := count(r, "max_results"); max > 0 && count(r, "offset") > 0 {
		q.Set("page", strconv.Itoa(count(r, "offset")/max+1))
	}
	return call(http.MethodGet, base(r, "https://www.searchapi.io/api/v1/search")+"?"+q.Encode(), map[string]string{"Accept": "application/json"}, nil)
}

// Parse reads what SearchApi answered.
func (SearchAPI) Parse(_ context.Context, r media.Request, _ media.Client, status int, _ http.Header, raw []byte) (media.Response, error) {
	if status < 200 || status > 299 {
		return media.Response{}, refused(status)
	}
	items := gjson.GetBytes(raw, "organic_results")
	if !items.Exists() {
		items = gjson.GetBytes(raw, "top_stories")
	}
	out := []result{}
	for i, v := range items.Array() {
		if i >= count(r, "max_results") {
			break
		}
		out = append(out, item("searchapi", i+1, v.Get("title").String(), v.Get("link").String()).
			withSnippet(cmp.Or(v.Get("snippet").String(), v.Get("description").String())).
			withPublished(cmp.Or(v.Get("date").String(), v.Get("published_at").String())).
			withFavicon(v.Get("favicon").String()).
			withAuthor(v.Get("source").String()).
			withImage(v.Get("thumbnail").String()))
	}
	var total *int
	if t := gjson.GetBytes(raw, "search_information.total_results"); t.Exists() {
		if n, err := strconv.Atoi(t.String()); err == nil {
			total = &n
		}
	}
	return reply("searchapi", field(r, "query"), out, nil, usage{QueriesUsed: 1, SearchCost: cost(r)}, total, nil)
}
