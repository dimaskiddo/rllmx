// Package ollama serves a local daemon's embeddings, which speak the OpenAI
// body and take no key.
package ollama

import (
	"context"
	"net/http"
	"strings"

	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
)

// Embeddings speaks the daemon's /v1/embeddings body.
type Embeddings struct{}

// Kind is the service it serves.
func (Embeddings) Kind() string { return upstream.KindEmbedding }

// Build makes the call; a daemon with no key sends no credential.
func (Embeddings) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	// The account's own host wins; a saved address may carry /v1 or the whole
	// endpoint, and either way the call goes to /v1/embeddings.
	base := strings.TrimRight(r.Account["baseUrl"], "/")
	if base == "" {
		base = strings.TrimRight(media.BaseURL(r), "/")
	}
	if base == "" {
		base = "http://localhost:11434"
	}
	base = strings.TrimSuffix(base, "/v1/embeddings")
	base = strings.TrimSuffix(base, "/v1")
	base += "/v1/embeddings"
	body, err := media.WithModel(r.Body, r.WireModel)
	if err != nil {
		return media.Call{}, err
	}
	h := map[string]string{"Content-Type": "application/json"}
	if r.Credential != "" {
		h["Authorization"] = "Bearer " + r.Credential
	}
	return media.Call{Method: http.MethodPost, URL: base, Header: h, Body: body}, nil
}

// Parse passes the list through.
func (Embeddings) Parse(_ context.Context, _ media.Request, _ media.Client, status int, header http.Header, body []byte) (media.Response, error) {
	return media.PassJSON(status, header, body, "the daemon answered no list")
}
