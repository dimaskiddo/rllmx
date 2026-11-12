package tts

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"rllmx/app/llm/media"
)

// ElevenLabs speaks its text-to-speech route keyed by voice id.
type ElevenLabs struct{}

// Kind is the service it serves.
func (ElevenLabs) Kind() string { return kind() }

// Build makes the call.
func (ElevenLabs) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	model, voiceID := voice(r, "eleven_flash_v2_5", "", r.Model)
	if voiceID == "" || strings.ContainsAny(voiceID, "/?# ") {
		return media.Call{}, media.BadRequestf("the request names no voice")
	}
	req := map[string]any{
		"text": body(r).Get("input").String(), "model_id": model,
		"voice_settings": map[string]float64{"stability": 0.5, "similarity_boost": 0.75},
	}
	raw, err := jsonBody(req)
	if err != nil {
		return media.Call{}, err
	}
	base := strings.TrimRight(media.BaseURL(r), "/")
	if base == "" {
		base = "https://api.elevenlabs.io/v1/text-to-speech"
	}
	if !strings.Contains(base, "/text-to-speech") {
		base += "/text-to-speech"
	}
	h := map[string]string{"Content-Type": "application/json", "Accept": "audio/mpeg"}
	if r.Credential != "" {
		h["xi-api-key"] = r.Credential
	}
	return media.Call{Method: http.MethodPost, URL: base + "/" + url.PathEscape(voiceID), Header: h, Body: raw}, nil
}

// Parse passes the mp3 through.
func (ElevenLabs) Parse(_ context.Context, _ media.Request, _ media.Client, status int, header http.Header, audio []byte) (media.Response, error) {
	return audioAnswer(status, header, audio, "mp3")
}
