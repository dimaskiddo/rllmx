package stt

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
)

// Gemini sends the audio inline and asks for a transcript.
type Gemini struct{}

// Kind is the service it serves.
func (Gemini) Kind() string { return kind() }

// Build makes the call.
func (Gemini) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	prompt := field(r, "prompt")
	if prompt == "" {
		prompt = "Generate a transcript of the speech. Return only the transcribed text, no commentary."
	}
	if language := field(r, "language"); language != "" {
		prompt += " Language: " + language + "."
	}
	payload, err := json.Marshal(map[string]any{
		"contents": []map[string]any{{"parts": []map[string]any{
			{"text": prompt},
			{"inline_data": map[string]string{"mime_type": audioMIME(r), "data": base64.StdEncoding.EncodeToString(r.Upload.Audio)}},
		}}},
	})
	if err != nil {
		return media.Call{}, media.BadRequestf("the upload cannot be built")
	}
	base := strings.TrimRight(media.BaseURL(r), "/")
	if base == "" {
		return media.Call{}, media.BadRequestf("the provider names no address")
	}
	q := url.Values{}
	if r.Credential != "" {
		q.Set("key", r.Credential)
	}
	target := base + "/" + url.PathEscape(field(r, "model")) + ":generateContent"
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	return media.Call{Method: http.MethodPost, URL: target, Header: map[string]string{"Content-Type": "application/json"}, Body: payload}, nil
}

// Parse joins the answer's text parts.
func (Gemini) Parse(_ context.Context, _ media.Request, _ media.Client, status int, _ http.Header, body []byte) (media.Response, error) {
	if status < 200 || status > 299 {
		return media.Response{}, refusal(status, "the provider refused the transcription")
	}
	var text strings.Builder
	for _, part := range gjson.GetBytes(body, "candidates.0.content.parts").Array() {
		text.WriteString(part.Get("text").String())
	}
	raw, _ := json.Marshal(map[string]string{"text": text.String()})
	return media.Response{Status: http.StatusOK, Header: map[string]string{"Content-Type": "application/json"}, Body: raw}, nil
}
