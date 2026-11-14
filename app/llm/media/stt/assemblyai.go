package stt

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
)

// AssemblyAI uploads the audio, submits a job and polls it.
type AssemblyAI struct{}

// Kind is the service it serves.
func (AssemblyAI) Kind() string { return kind() }

// Build uploads the audio; Parse submits and polls.
func (AssemblyAI) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	base := media.BaseURL(r)
	if base == "" {
		return media.Call{}, media.BadRequestf("the provider names no address")
	}
	h := map[string]string{"Content-Type": "application/octet-stream"}
	if r.Credential != "" {
		h["Authorization"] = r.Credential
	}
	return media.Call{Method: http.MethodPost, URL: strings.TrimSuffix(base, "/transcript") + "/upload", Header: h, Body: r.Upload.Audio}, nil
}

// Parse submits the job and polls until it finishes.
func (AssemblyAI) Parse(ctx context.Context, r media.Request, c media.Client, status int, header http.Header, body []byte) (media.Response, error) {
	if status < 200 || status > 299 {
		return media.Response{}, refusal(status, "the provider refused the upload")
	}
	uploadURL := gjson.GetBytes(body, "upload_url").String()
	if uploadURL == "" {
		return media.Response{}, refusal(http.StatusBadGateway, "the provider answered no upload")
	}
	base := media.BaseURL(r)
	h := map[string]string{"Content-Type": "application/json"}
	if r.Credential != "" {
		h["Authorization"] = r.Credential
	}
	request, _ := json.Marshal(map[string]any{
		"audio_url": uploadURL, "speech_models": []string{field(r, "model")}, "language_detection": true,
	})
	st, _, submitted, err := c.Do(ctx, http.MethodPost, base, h, request)
	if err != nil {
		return media.Response{}, err
	}
	if st < 200 || st > 299 {
		return media.Response{}, refusal(st, "the provider refused the transcription")
	}
	id := gjson.GetBytes(submitted, "id").String()
	if id == "" || strings.ContainsAny(id, "/?#") {
		return media.Response{}, refusal(http.StatusBadGateway, "the provider answered no job")
	}
	var out media.Response
	err = media.Poll(ctx, func(ctx context.Context) (bool, error) {
		s, _, polled, err := c.Do(ctx, http.MethodGet, base+"/"+id, h, nil)
		if err != nil || s < 200 || s > 299 {
			return false, nil
		}
		switch gjson.GetBytes(polled, "status").String() {
		case "completed":
			out, err = textAnswer(http.StatusOK, polled)
			return true, err
		case "error":
			return false, refusal(http.StatusBadGateway, "the provider could not transcribe the audio")
		}
		return false, nil
	})
	return out, err
}
