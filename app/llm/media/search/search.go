// Package search serves /v1/search: a vendor's own search API, or the provider's
// chat endpoint with its built-in search. Every adapter answers the one shape the
// route returns.
package search

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
)

// result is one normalized hit.
type result struct {
	Title       string   `json:"title"`
	URL         string   `json:"url"`
	DisplayURL  string   `json:"display_url,omitempty"`
	Snippet     string   `json:"snippet"`
	Position    int      `json:"position"`
	Score       *float64 `json:"score"`
	PublishedAt *string  `json:"published_at"`
	FaviconURL  *string  `json:"favicon_url"`
	Content     *content `json:"content"`
	Metadata    metadata `json:"metadata"`
	Citation    citation `json:"citation"`
	ProviderRaw any      `json:"provider_raw"`
}

type content struct {
	Format string `json:"format"`
	Text   string `json:"text"`
	Length int    `json:"length"`
}

type metadata struct {
	Author     *string `json:"author"`
	Language   *string `json:"language"`
	SourceType *string `json:"source_type,omitempty"`
	ImageURL   *string `json:"image_url,omitempty"`
}

type citation struct {
	Provider    string `json:"provider"`
	RetrievedAt string `json:"retrieved_at"`
	Rank        int    `json:"rank"`
}

type answer struct {
	Source string `json:"source"`
	Text   string `json:"text"`
	Model  string `json:"model"`
}

type usage struct {
	QueriesUsed int      `json:"queries_used"`
	SearchCost  *float64 `json:"search_cost_usd"`
	Credits     *int     `json:"provider_credits_used,omitempty"`
	LLMTokens   int64    `json:"llm_tokens,omitempty"`
}

type metrics struct {
	ResponseTimeMs    int64 `json:"response_time_ms"`
	UpstreamLatencyMs int64 `json:"upstream_latency_ms"`
	TotalResults      *int  `json:"total_results_available"`
}

type pagination struct {
	HasMore    bool    `json:"has_more"`
	NextCursor *string `json:"next_cursor"`
}

// envelope is the route's answer.
type envelope struct {
	Provider   string      `json:"provider"`
	Query      string      `json:"query"`
	Results    []result    `json:"results"`
	Answer     *answer     `json:"answer"`
	Usage      usage       `json:"usage"`
	Metrics    metrics     `json:"metrics"`
	Pagination *pagination `json:"pagination,omitempty"`
	Errors     []string    `json:"errors"`
}

// reply builds the answer. Timing is filled by the pipeline, which knows when
// the request started.
func reply(provider, query string, results []result, ans *answer, u usage, total *int, pag *pagination) (media.Response, error) {
	if results == nil {
		results = []result{}
	}
	raw, err := json.Marshal(envelope{
		Provider: provider, Query: query, Results: results, Answer: ans, Usage: u,
		Metrics: metrics{TotalResults: total}, Pagination: pag, Errors: []string{},
	})
	if err != nil {
		return media.Response{}, media.BadRequestf("the answer cannot be built")
	}
	return media.Response{Status: http.StatusOK, Header: jsonHeader(), Body: raw}, nil
}

func jsonHeader() map[string]string {
	return map[string]string{"Content-Type": "application/json"}
}

// refused is a provider's own refusal, read before the gateway classifies it.
func refused(status int) error {
	return &media.ProviderError{Status: status, Text: "the provider refused the search"}
}

// hit starts a result at the position the provider gave it.
func hit(provider string, position int) result {
	return result{Position: position, Citation: citation{Provider: provider, RetrievedAt: time.Now().UTC().Format(time.RFC3339), Rank: position}}
}

// item is a result with the title and address a provider sent.
func item(provider string, position int, title, link string) result {
	r := hit(provider, position)
	r.Title, r.URL, r.DisplayURL = title, link, displayURL(link)
	return r
}

// text and score set what each provider may or may not send.
func (r result) withSnippet(s string) result { r.Snippet = s; return r }

func (r result) withScore(v float64) result {
	if v < 0 {
		v = 0
	}
	if v > 1 {
		v = 1
	}
	r.Score = &v
	return r
}

func (r result) withPublished(v string) result {
	if v != "" {
		r.PublishedAt = &v
	}
	return r
}

func (r result) withFavicon(v string) result {
	if v != "" {
		r.FaviconURL = &v
	}
	return r
}

func (r result) withAuthor(v string) result {
	if v != "" {
		r.Metadata.Author = &v
	}
	return r
}

func (r result) withImage(v string) result {
	if v != "" {
		r.Metadata.ImageURL = &v
	}
	return r
}

func (r result) withSource(v string) result {
	if v != "" {
		r.Metadata.SourceType = &v
	}
	return r
}

func (r result) withContent(format, text string) result {
	if text != "" {
		r.Content = &content{Format: format, Text: text, Length: len(text)}
	}
	return r
}

// displayURL is the address a client shows: no scheme, no www, no query.
func displayURL(link string) string {
	link = strings.TrimPrefix(strings.TrimPrefix(link, "https://"), "http://")
	link = strings.TrimPrefix(link, "www.")
	if i := strings.IndexByte(link, '?'); i >= 0 {
		link = link[:i]
	}
	return link
}

// body reads the canonical request the pipeline built.
func body(r media.Request) gjson.Result { return gjson.ParseBytes(r.Body) }

func field(r media.Request, name string) string { return body(r).Get(name).String() }

func count(r media.Request, name string) int { return int(body(r).Get(name).Int()) }

// headers places the credential where the provider's data says it goes.
func headers(r media.Request, query url.Values) map[string]string {
	h := media.Headers(r, query)
	h["Accept"] = "application/json"
	return h
}

// call is one provider request: a method, an address and an optional body.
func call(method, target string, h map[string]string, payload any) (media.Call, error) {
	var raw []byte
	if payload != nil {
		var err error
		if raw, err = json.Marshal(payload); err != nil {
			return media.Call{}, media.BadRequestf("the request cannot be built")
		}
		h["Content-Type"] = "application/json"
	}
	if h == nil {
		h = map[string]string{}
	}
	return media.Call{Method: method, URL: target, Header: h, Body: raw}, nil
}

// firstOf is the first non-empty value, which is how a vendor's own answer is
// read when it may carry one of two names for a field.
func firstOf(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// domainFilter splits the client's list the way every provider that takes one
// does: an entry with a leading dash is an exclusion.
func domainFilter(r media.Request) (includes, excludes []string) {
	for _, v := range body(r).Get("domain_filter").Array() {
		s := v.String()
		if s == "" {
			continue
		}
		if s[0] == '-' {
			excludes = append(excludes, s[1:])
			continue
		}
		includes = append(includes, s)
	}
	return includes, excludes
}

// truncateRunes cuts text to at most n runes.
func truncateRunes(text string, n int) string {
	if len(text) <= n {
		return text
	}
	runes := []rune(text)
	if len(runes) <= n {
		return text
	}
	return string(runes[:n])
}

// base is the provider's address with no trailing slash.
func base(r media.Request, fallback string) string {
	if r.Config != nil && r.Config.BaseURL != "" {
		return strings.TrimRight(r.Config.BaseURL, "/")
	}
	return strings.TrimRight(fallback, "/")
}

// cost is the per-query price the provider's data names.
func cost(r media.Request) *float64 {
	if r.Config == nil {
		return nil
	}
	v := r.Config.CostPerQuery
	return &v
}

// costOf is the providers that charge nothing per query.
func free() *float64 {
	v := 0.0
	return &v
}
