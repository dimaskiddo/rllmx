package search

import (
	"cmp"
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
)

// Linkup searches Linkup's index.
type Linkup struct{}

// Kind is the service it serves.
func (Linkup) Kind() string { return upstream.KindSearch }

// Build makes the search call.
func (Linkup) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	if r.Credential == "" {
		return media.Call{}, media.BadRequestf("this provider needs an API key")
	}
	includes, excludes := domainFilter(r)
	depth := r.Account["depth"]
	if depth != "fast" && depth != "standard" && depth != "deep" {
		depth = "standard"
	}
	payload := map[string]any{
		"q": field(r, "query"), "depth": depth, "outputType": "searchResults",
		"maxResults": count(r, "max_results"),
	}
	if len(includes) > 0 {
		payload["includeDomains"] = includes
	}
	if len(excludes) > 0 {
		payload["excludeDomains"] = excludes
	}
	if from, to := timeRange(field(r, "time_range"), time.Now().UTC()); from != "" {
		payload["fromDate"], payload["toDate"] = from, to
	}
	return call(http.MethodPost, base(r, "https://api.linkup.so/v1/search"), headers(r, nil), payload)
}

// Parse reads what Linkup answered.
func (Linkup) Parse(_ context.Context, r media.Request, _ media.Client, status int, _ http.Header, raw []byte) (media.Response, error) {
	if status < 200 || status > 299 {
		return media.Response{}, refused(status)
	}
	out := []result{}
	for i, v := range gjson.GetBytes(raw, "results").Array() {
		if i >= count(r, "max_results") {
			break
		}
		text := v.Get("content").String()
		out = append(out, item("linkup", i+1, cmp.Or(v.Get("name").String(), v.Get("title").String()), v.Get("url").String()).
			withSnippet(cmp.Or(text, v.Get("snippet").String())).
			withSource(cmp.Or(v.Get("type").String(), "web")).
			withImage(cmp.Or(v.Get("image_url").String(), v.Get("imageUrl").String())).
			withContent("text", text))
	}
	total := len(out)
	return reply("linkup", field(r, "query"), out, nil, usage{QueriesUsed: 1, SearchCost: cost(r)}, &total, nil)
}

// timeRange turns the client's word into the pair of dates a provider takes.
func timeRange(word string, now time.Time) (from, to string) {
	var start time.Time
	switch strings.ToLower(word) {
	case "day":
		start = now.AddDate(0, 0, -1)
	case "week":
		start = now.AddDate(0, 0, -7)
	case "month":
		start = now.AddDate(0, -1, 0)
	case "year":
		start = now.AddDate(-1, 0, 0)
	default:
		return "", ""
	}
	return start.Format(time.DateOnly), now.Format(time.DateOnly)
}
