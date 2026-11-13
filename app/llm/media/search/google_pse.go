package search

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
)

// GooglePSE searches Google's Programmable Search engine, which needs the
// account's engine id beside its key.
type GooglePSE struct{}

// Kind is the service it serves.
func (GooglePSE) Kind() string { return upstream.KindSearch }

// Build makes the search call.
func (GooglePSE) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	cx := r.Account["cx"]
	if r.Credential == "" || cx == "" {
		return media.Call{}, media.BadRequestf("this provider needs both an API key and a search engine id")
	}
	limit := min(count(r, "max_results"), 10)
	q := url.Values{"key": {r.Credential}, "cx": {cx}, "q": {field(r, "query")}, "num": {strconv.Itoa(limit)}}
	if v := field(r, "country"); v != "" {
		q.Set("gl", v)
	}
	if v := field(r, "language"); v != "" {
		q.Set("hl", v)
	}
	switch field(r, "time_range") {
	case "day":
		q.Set("dateRestrict", "d1")
	case "week":
		q.Set("dateRestrict", "w1")
	case "month":
		q.Set("dateRestrict", "m1")
	case "year":
		q.Set("dateRestrict", "y1")
	}
	if offset := count(r, "offset"); offset > 0 {
		q.Set("start", strconv.Itoa(min(offset+1, 91)))
	}
	return call(http.MethodGet, base(r, "https://www.googleapis.com/customsearch/v1")+"?"+q.Encode(), map[string]string{"Accept": "application/json"}, nil)
}

// Parse reads what the engine answered.
func (GooglePSE) Parse(_ context.Context, r media.Request, _ media.Client, status int, _ http.Header, raw []byte) (media.Response, error) {
	if status < 200 || status > 299 {
		return media.Response{}, refused(status)
	}
	out := []result{}
	for i, v := range gjson.GetBytes(raw, "items").Array() {
		if i >= count(r, "max_results") {
			break
		}
		out = append(out, item("google-pse", i+1, v.Get("title").String(), v.Get("link").String()).
			withSnippet(v.Get("snippet").String()).
			withImage(firstOf(v.Get("pagemap.cse_image.0.src").String(), v.Get("pagemap.cse_thumbnail.0.src").String(), v.Get("pagemap.metatags.0.og:image").String())))
	}
	var total *int
	if t := gjson.GetBytes(raw, "searchInformation.totalResults"); t.Exists() {
		if n, err := strconv.Atoi(t.String()); err == nil {
			total = &n
		}
	}
	return reply("google-pse", field(r, "query"), out, nil, usage{QueriesUsed: 1, SearchCost: cost(r)}, total, nil)
}
