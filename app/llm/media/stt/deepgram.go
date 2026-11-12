package stt

import (
	"context"
	"net/http"
	"net/url"

	"rllmx/app/llm/media"
)

// Deepgram takes the raw audio and a query of options.
type Deepgram struct{}

// Kind is the service it serves.
func (Deepgram) Kind() string { return kind() }

// Build makes the call.
func (Deepgram) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	q := url.Values{}
	q.Set("model", field(r, "model"))
	q.Set("smart_format", "true")
	q.Set("punctuate", "true")
	if language := field(r, "language"); language != "" {
		q.Set("language", language)
	} else {
		q.Set("detect_language", "true")
	}
	target := media.BaseURL(r)
	if target == "" {
		return media.Call{}, media.BadRequestf("the provider names no address")
	}
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	h := map[string]string{"Content-Type": audioMIME(r)}
	if r.Credential != "" {
		h["Authorization"] = "Token " + r.Credential
	}
	return media.Call{Method: http.MethodPost, URL: target, Header: h, Body: r.Upload.Audio}, nil
}

// Parse reads the transcript.
func (Deepgram) Parse(_ context.Context, _ media.Request, _ media.Client, status int, _ http.Header, body []byte) (media.Response, error) {
	return textAnswer(status, body)
}
