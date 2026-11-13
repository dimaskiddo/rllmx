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

// Xquik searches X through Xquik's index.
type Xquik struct{}

// Kind is the service it serves.
func (Xquik) Kind() string { return upstream.KindSearch }

// Build makes the search call.
func (Xquik) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	if r.Credential == "" {
		return media.Call{}, media.BadRequestf("this provider needs an API key")
	}
	q := url.Values{"q": {field(r, "query")}, "limit": {strconv.Itoa(count(r, "max_results"))}}
	if v := r.Account["cursor"]; v != "" {
		q.Set("cursor", v)
	}
	if v := r.Account["queryType"]; v != "" {
		if v != "Latest" && v != "Top" {
			return media.Call{}, media.BadRequestf("the configured query type is not one this provider takes")
		}
		q.Set("queryType", v)
	}
	if v := field(r, "language"); v != "" {
		q.Set("language", v)
	}
	return call(http.MethodGet, base(r, "https://xquik.com/api/v1/x/tweets/search")+"?"+q.Encode(), headers(r, nil), nil)
}

// Parse reads what Xquik answered, one post per result.
func (Xquik) Parse(_ context.Context, r media.Request, _ media.Client, status int, _ http.Header, raw []byte) (media.Response, error) {
	if status < 200 || status > 299 {
		return media.Response{}, refused(status)
	}
	limit := count(r, "max_results")
	out := []result{}
	for i, v := range gjson.GetBytes(raw, "tweets").Array() {
		if i >= limit {
			break
		}
		user := v.Get("author.username").String()
		id := v.Get("id").String()
		link := ""
		switch {
		case user != "" && id != "":
			link = "https://x.com/" + url.PathEscape(user) + "/status/" + url.PathEscape(id)
		case id != "":
			link = "https://x.com/i/web/status/" + url.PathEscape(id)
		}
		title := "X post"
		if user != "" {
			title = "@" + user + " on X"
		}
		text := v.Get("text").String()
		out = append(out, item("xquik", i+1, title, link).
			withSnippet(text).
			withPublished(v.Get("createdAt").String()).
			withAuthor(firstOf(user, v.Get("author.name").String())).
			withImage(v.Get("media.0.mediaUrl").String()).
			withSource("x_post").
			withContent("text", text))
	}
	var next *string
	if c := gjson.GetBytes(raw, "next_cursor").String(); c != "" {
		next = &c
	}
	pag := &pagination{HasMore: gjson.GetBytes(raw, "has_next_page").Bool(), NextCursor: next}
	return reply("xquik", field(r, "query"), out, nil, usage{QueriesUsed: 1, SearchCost: cost(r)}, nil, pag)
}
