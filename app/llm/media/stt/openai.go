package stt

import (
	"context"
	"net/http"

	"rllmx/app/llm/media"
)

// OpenAI speaks the multipart transcription body.
type OpenAI struct{}

// Kind is the service it serves.
func (OpenAI) Kind() string { return kind() }

// Build makes the call.
func (OpenAI) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	fields := []struct{ form, upstream string }{
		{"model", "model"}, {"language", "language"}, {"prompt", "prompt"},
		{"response_format", "response_format"}, {"temperature", "temperature"},
	}
	return multipartCall(r, media.BaseURL(r), bearer(r), fields, nil)
}

// Parse passes the answer: JSON, text, srt or vtt as the provider wrote it.
func (OpenAI) Parse(_ context.Context, _ media.Request, _ media.Client, status int, header http.Header, body []byte) (media.Response, error) {
	return passthrough(status, header, body)
}
