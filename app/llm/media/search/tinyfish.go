package search

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
)

// TinyFish searches its own index, ten results to a page.
type TinyFish struct{}

// Kind is the service it serves.
func (TinyFish) Kind() string { return upstream.KindSearch }

// Build makes the search call. The key stays on its own endpoint, because a
// client-chosen address must never receive it.
func (TinyFish) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	q := url.Values{"query": {field(r, "query")}}
	if t := field(r, "search_type"); t != "" && t != "web" {
		q.Set("domain_type", t)
	}
	if v := field(r, "country"); v != "" {
		q.Set("location", v)
	}
	if v := field(r, "language"); v != "" {
		q.Set("language", v)
	}
	includes, excludes := domainFilter(r)
	if len(includes) > 0 {
		q.Set("include_domains", strings.Join(includes, ","))
	}
	if len(excludes) > 0 {
		q.Set("exclude_domains", strings.Join(excludes, ","))
	}
	if offset := count(r, "offset"); offset > 0 {
		if offset >= 110 {
			return media.Call{}, media.BadRequestf("the offset is past this provider's pages")
		}
		if offset%10+count(r, "max_results") > 10 {
			return media.Call{}, media.BadRequestf("the offset and count must fit in one page")
		}
		q.Set("page", strconv.Itoa(offset/10))
	}
	return call(http.MethodGet, base(r, "https://api.search.tinyfish.ai")+"?"+q.Encode(), headers(r, nil), nil)
}

// Parse reads what TinyFish answered.
func (TinyFish) Parse(_ context.Context, r media.Request, _ media.Client, status int, _ http.Header, raw []byte) (media.Response, error) {
	if status < 200 || status > 299 {
		return media.Response{}, refused(status)
	}
	out := []result{}
	for i, v := range gjson.GetBytes(raw, "results").Array() {
		if i >= count(r, "max_results") {
			break
		}
		author := v.Get("authors.0").String()
		if authors := v.Get("authors").Array(); len(authors) > 1 {
			var names []string
			for _, a := range authors {
				names = append(names, a.String())
			}
			author = strings.Join(names, ", ")
		}
		out = append(out, item("tinyfish", i+1, v.Get("title").String(), v.Get("url").String()).
			withSnippet(v.Get("snippet").String()).
			withPublished(v.Get("date").String()).
			withAuthor(author).
			withSource(v.Get("publisher").String()))
	}
	var total *int
	if t := gjson.GetBytes(raw, "total_results"); t.Type == gjson.Number {
		n := int(t.Int())
		total = &n
	}
	return reply("tinyfish", field(r, "query"), out, nil, usage{QueriesUsed: 1, SearchCost: cost(r)}, total, nil)
}
