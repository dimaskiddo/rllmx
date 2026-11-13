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

// YouCom searches You.com's index.
type YouCom struct{}

// Kind is the service it serves.
func (YouCom) Kind() string { return upstream.KindSearch }

// Build makes the search call.
func (YouCom) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	if r.Credential == "" {
		return media.Call{}, media.BadRequestf("this provider needs an API key")
	}
	includes, excludes := domainFilter(r)
	limit := min(count(r, "max_results"), 100)
	q := url.Values{"query": {field(r, "query")}, "count": {strconv.Itoa(limit)}}
	if v := field(r, "time_range"); v != "" && v != "any" {
		q.Set("freshness", v)
	}
	if offset := count(r, "offset"); offset > 0 && limit > 0 {
		q.Set("offset", strconv.Itoa(min(offset/limit, 9)))
	}
	if v := field(r, "country"); v != "" {
		q.Set("country", v)
	}
	if v := field(r, "language"); v != "" {
		q.Set("language", v)
	}
	if len(includes) > 0 {
		q.Set("include_domains", strings.Join(includes, ","))
	}
	if len(excludes) > 0 {
		q.Set("exclude_domains", strings.Join(excludes, ","))
	}
	return call(http.MethodGet, base(r, "https://ydc-index.io/v1/search")+"?"+q.Encode(), headers(r, nil), nil)
}

// Parse reads what You.com answered.
func (YouCom) Parse(_ context.Context, r media.Request, _ media.Client, status int, _ http.Header, raw []byte) (media.Response, error) {
	if status < 200 || status > 299 {
		return media.Response{}, refused(status)
	}
	section := gjson.GetBytes(raw, "results.web")
	if field(r, "search_type") == "news" {
		section = gjson.GetBytes(raw, "results.news")
	}
	out := []result{}
	for i, v := range section.Array() {
		if i >= count(r, "max_results") {
			break
		}
		snippet := v.Get("snippets.0").String()
		if snippet == "" {
			snippet = v.Get("description").String()
		}
		format, text := "", ""
		switch {
		case v.Get("markdown").Type == gjson.String:
			format, text = "markdown", v.Get("markdown").String()
		case v.Get("html").Type == gjson.String:
			format, text = "html", v.Get("html").String()
		}
		out = append(out, item("youcom", i+1, v.Get("title").String(), v.Get("url").String()).
			withSnippet(snippet).
			withPublished(v.Get("page_age").String()).
			withFavicon(v.Get("favicon_url").String()).
			withImage(v.Get("thumbnail_url").String()).
			withSource(field(r, "search_type")).
			withContent(format, text))
	}
	total := len(out)
	return reply("youcom", field(r, "query"), out, nil, usage{QueriesUsed: 1, SearchCost: cost(r)}, &total, nil)
}
