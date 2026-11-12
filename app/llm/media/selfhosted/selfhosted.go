// Package selfhosted serves a self-hosted embeddings server through the account's
// own address. It refuses a connection without one instead of falling back to a
// public endpoint, where the operator's text would leave the machine.
package selfhosted

import (
	"context"
	"net/http"
	"strings"

	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
)

// Embeddings speaks the OpenAI embeddings body at the account's address.
type Embeddings struct{}

// Kind is the service it serves.
func (Embeddings) Kind() string { return upstream.KindEmbedding }

// Build makes the call, refusing a connection that names no address.
func (Embeddings) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	base := strings.TrimRight(r.Account["baseUrl"], "/")
	if base == "" {
		return media.Call{}, media.BadRequestf("the connection names no address")
	}
	if !strings.HasSuffix(base, "/embeddings") {
		base += "/embeddings"
	}
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
	return media.PassJSON(status, header, body, "the server answered no list")
}
