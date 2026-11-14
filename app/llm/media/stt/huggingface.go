package stt

import (
	"context"
	"net/http"
	"strings"

	"rllmx/app/llm/media"
)

// HuggingFace posts the raw audio to one model route.
type HuggingFace struct{}

// Kind is the service it serves.
func (HuggingFace) Kind() string { return kind() }

// Build makes the call, refusing a model path that could move the address.
func (HuggingFace) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	model := field(r, "model")
	if model == "" {
		model = r.Model
	}
	if strings.Contains(model, "..") || strings.Contains(model, "//") || strings.ContainsAny(model, "?#") {
		return media.Call{}, media.BadRequestf("the model names an address")
	}
	target := media.BaseURL(r) + "/" + model
	h := bearer(r)
	h["Content-Type"] = audioMIME(r)
	return media.Call{Method: http.MethodPost, URL: target, Header: h, Body: r.Upload.Audio}, nil
}

// Parse reads the transcript.
func (HuggingFace) Parse(_ context.Context, _ media.Request, _ media.Client, status int, _ http.Header, body []byte) (media.Response, error) {
	return textAnswer(status, body)
}
