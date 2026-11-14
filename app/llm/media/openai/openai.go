// Package openai serves the OpenAI-shaped media families: embeddings and image
// generation, for OpenAI itself and every provider that copied its body.
package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
)

// Embeddings speaks the OpenAI embeddings body.
type Embeddings struct{}

// Kind is the service it serves.
func (Embeddings) Kind() string { return upstream.KindEmbedding }

// Build makes the embeddings call.
func (Embeddings) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	base := media.BaseURL(r)
	if base == "" {
		return media.Call{}, media.BadRequestf("the provider names no address")
	}
	if !strings.HasSuffix(base, "/embeddings") {
		base += "/embeddings"
	}
	body, err := embeddingBody(r.Body, r.WireModel)
	if err != nil {
		return media.Call{}, err
	}
	q := url.Values{}
	h := media.Headers(r, q)
	h["Content-Type"] = "application/json"
	return media.Call{Method: http.MethodPost, URL: media.WithQuery(base, q), Header: h, Body: body}, nil
}

// Parse reads the list and its usage.
func (Embeddings) Parse(_ context.Context, _ media.Request, _ media.Client, status int, _ http.Header, body []byte) (media.Response, error) {
	if status < 200 || status > 299 {
		return media.Response{}, &media.ProviderError{Status: status, Text: "the provider refused the embedding"}
	}
	return media.PassJSON(status, http.Header{"Content-Type": []string{"application/json"}}, body, "the provider answered no list")
}

// Images speaks the OpenAI image-generation body, with the provider's own field
// filter when its config names one.
type Images struct{}

// Kind is the service it serves.
func (Images) Kind() string { return upstream.KindImage }

// Build makes the generation call.
func (Images) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	base := media.BaseURL(r)
	if base == "" {
		return media.Call{}, media.BadRequestf("the provider names no address")
	}
	body, err := media.WithModel(r.Body, r.WireModel)
	if err != nil {
		return media.Call{}, err
	}
	if f := r.Config; f != nil && len(f.BodyFields) > 0 {
		if body, err = onlyFields(body, f.BodyFields); err != nil {
			return media.Call{}, err
		}
	}
	q := url.Values{}
	h := media.Headers(r, q)
	h["Content-Type"] = "application/json"
	return media.Call{Method: http.MethodPost, URL: media.WithQuery(base, q), Header: h, Body: body}, nil
}

// Parse passes the OpenAI list through and reads its usage when reported.
func (Images) Parse(_ context.Context, _ media.Request, _ media.Client, status int, header http.Header, body []byte) (media.Response, error) {
	if status < 200 || status > 299 {
		return media.Response{}, &media.ProviderError{Status: status, Text: "the provider refused the image"}
	}
	if !gjson.ValidBytes(body) {
		return media.Response{}, &media.ProviderError{Status: http.StatusBadGateway, Text: "the provider answered no image"}
	}
	res, err := media.PassJSON(status, header, body, "the provider answered no image")
	res.Usage = media.ImageUsageOf(body)
	return res, err
}

// embeddingFields are the fields the embeddings API reads; anything else the
// client sent is not forwarded.
var embeddingFields = []string{"input", "encoding_format", "dimensions", "user"}

// embeddingBody rebuilds the request from those fields and the provider's model.
func embeddingBody(body []byte, wire string) ([]byte, error) {
	if !gjson.ValidBytes(body) {
		return nil, media.BadRequestf("the request body is not JSON")
	}
	out := map[string]any{}
	for _, f := range embeddingFields {
		if v := gjson.GetBytes(body, f); v.Exists() {
			out[f] = v.Value()
		}
	}
	if wire != "" {
		out["model"] = wire
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return nil, media.BadRequestf("the request cannot be built")
	}
	return raw, nil
}

// onlyFields keeps the named top-level fields of the client's body.
func onlyFields(body []byte, fields []string) ([]byte, error) {
	if !gjson.ValidBytes(body) {
		return nil, media.BadRequestf("the request body is not JSON")
	}
	obj := gjson.ParseBytes(body)
	kept := map[string]json.RawMessage{}
	for _, f := range fields {
		if v := obj.Get(f); v.Exists() {
			kept[f] = json.RawMessage(v.Raw)
		}
	}
	raw, err := json.Marshal(kept)
	if err != nil {
		return nil, media.BadRequestf("the request body cannot be rebuilt")
	}
	return raw, nil
}
