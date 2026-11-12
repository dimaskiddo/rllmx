// Package github serves Copilot's embeddings endpoint, which speaks the OpenAI
// body behind the client's own identity headers.
package github

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
)

// Embeddings speaks the OpenAI embeddings body at Copilot's address.
type Embeddings struct{}

// Kind is the service it serves.
func (Embeddings) Kind() string { return upstream.KindEmbedding }

// Build makes the call with the account's Copilot token and the client headers.
func (Embeddings) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	base := strings.TrimRight(media.BaseURL(r), "/")
	if base == "" {
		base = "https://api.githubcopilot.com/embeddings"
	}
	body, err := media.WithModel(r.Body, r.WireModel)
	if err != nil {
		return media.Call{}, err
	}
	q := url.Values{}
	h := media.Headers(r, q)
	for k, v := range r.Entry.Transport.Headers {
		h[k] = v
	}
	h["Content-Type"] = "application/json"
	return media.Call{Method: http.MethodPost, URL: media.WithQuery(base, q), Header: h, Body: body}, nil
}

// Parse passes the OpenAI list through.
func (Embeddings) Parse(_ context.Context, _ media.Request, _ media.Client, status int, header http.Header, body []byte) (media.Response, error) {
	return media.PassJSON(status, header, body, "the provider answered no list")
}
