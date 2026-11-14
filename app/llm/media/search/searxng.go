package search

import (
	"cmp"
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
)

// SearXNG searches the operator's own instance, which takes no credential and
// lives wherever MEDIA_SEARXNG_URL points.
type SearXNG struct{}

// Kind is the service it serves.
func (SearXNG) Kind() string { return upstream.KindSearch }

// Build makes the search call.
func (SearXNG) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	target := base(r, "")
	if target == "" {
		return media.Call{}, media.BadRequestf("this instance has no address")
	}
	if !strings.HasSuffix(target, "/search") {
		target += "/search"
	}
	categories := "general"
	if field(r, "search_type") == "news" {
		categories = "news"
	}
	q := url.Values{"q": {field(r, "query")}, "format": {"json"}, "categories": {categories}}
	if v := field(r, "language"); v != "" {
		q.Set("language", v)
	}
	if v := field(r, "time_range"); v != "" && v != "any" {
		q.Set("time_range", v)
	}
	if max := count(r, "max_results"); max > 0 && count(r, "offset") > 0 {
		q.Set("pageno", strconv.Itoa(count(r, "offset")/max+1))
	}
	return call(http.MethodGet, target+"?"+q.Encode(), map[string]string{"Accept": "application/json"}, nil)
}

// Parse reads what the instance answered.
func (SearXNG) Parse(_ context.Context, r media.Request, _ media.Client, status int, _ http.Header, raw []byte) (media.Response, error) {
	if status < 200 || status > 299 {
		return media.Response{}, refused(status)
	}
	out := []result{}
	for i, v := range gjson.GetBytes(raw, "results").Array() {
		if i >= count(r, "max_results") {
			break
		}
		engines := []string{}
		for _, e := range v.Get("engines").Array() {
			engines = append(engines, e.String())
		}
		out = append(out, item("searxng", i+1, v.Get("title").String(), v.Get("url").String()).
			withSnippet(cmp.Or(v.Get("content").String(), v.Get("snippet").String())).
			withPublished(cmp.Or(v.Get("publishedDate").String(), v.Get("published_date").String())).
			withSource(cmp.Or(strings.Join(engines, ", "), v.Get("engine").String(), v.Get("category").String())).
			withImage(cmp.Or(v.Get("thumbnail").String(), v.Get("img_src").String())))
	}
	total := len(out)
	return reply("searxng", field(r, "query"), out, nil, usage{QueriesUsed: 1, SearchCost: free()}, &total, nil)
}
