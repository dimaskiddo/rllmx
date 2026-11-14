// Package fetch serves /v1/web/fetch: a URL read by a vendor's extraction API,
// or by the built-in reader that converts a page itself.
package fetch

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
	apptext "rllmx/app/platform/text"
)

// envelope is the route's answer.
type envelope struct {
	Provider string   `json:"provider"`
	URL      string   `json:"url"`
	Title    *string  `json:"title"`
	Content  content  `json:"content"`
	Metadata metadata `json:"metadata"`
	Usage    usage    `json:"usage"`
	Metrics  metrics  `json:"metrics"`
	Links    []string `json:"links,omitempty"`
}

type content struct {
	Format string `json:"format"`
	Text   string `json:"text"`
	Length int    `json:"length"`
}

type metadata struct {
	Author      *string `json:"author"`
	PublishedAt *string `json:"published_at"`
	Language    *string `json:"language"`
}

type usage struct {
	FetchCost *float64 `json:"fetch_cost_usd"`
}

type metrics struct {
	ResponseTimeMs    int64 `json:"response_time_ms"`
	UpstreamLatencyMs int64 `json:"upstream_latency_ms"`
}

// reply builds the answer. Timing is filled by the pipeline, which knows when
// the request started.
func reply(provider, target, format, title, text string, links []string, limit int, cost *float64) (media.Response, error) {
	page := build(provider, target, format, title, text, links, limit, cost)
	return page.json()
}

// build assembles one page's answer without its timing.
func build(provider, target, format, title, text string, links []string, limit int, cost *float64) envelope {
	if limit > 0 {
		text = apptext.CutRunes(text, limit)
	}
	out := envelope{
		Provider: provider, URL: target, Content: content{Format: format, Text: text, Length: len(text)},
		Usage: usage{FetchCost: cost}, Links: links,
	}
	if title != "" {
		out.Title = &title
	}
	return out
}

func (e envelope) json() (media.Response, error) {
	raw, err := json.Marshal(e)
	if err != nil {
		return media.Response{}, media.BadRequestf("the answer cannot be built")
	}
	return media.Response{Status: http.StatusOK, Header: map[string]string{"Content-Type": "application/json"}, Body: raw}, nil
}

// refused is a provider's own refusal, read before the gateway classifies it.
func refused(status int) error {
	return &media.ProviderError{Status: status, Text: "the provider refused the page"}
}

// target reads the canonical request the pipeline built.
func target(r media.Request) string { return gjson.GetBytes(r.Body, "url").String() }

func format(r media.Request) string { return gjson.GetBytes(r.Body, "format").String() }

func limit(r media.Request) int { return int(gjson.GetBytes(r.Body, "max_characters").Int()) }

// address is the provider's endpoint with no trailing slash.
func address(r media.Request, fallback string) string {
	if r.Config != nil && r.Config.BaseURL != "" {
		return strings.TrimRight(r.Config.BaseURL, "/")
	}
	return strings.TrimRight(fallback, "/")
}

// cost is the per-call price the provider's data names.
func cost(r media.Request) *float64 {
	if r.Config == nil {
		return nil
	}
	v := r.Config.CostPerQuery
	return &v
}
