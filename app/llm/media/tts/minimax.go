package tts

import (
	"context"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
)

// MiniMax speaks its t2a endpoint, which answers hex.
type MiniMax struct{}

// Kind is the service it serves.
func (MiniMax) Kind() string { return kind() }

// Build makes the call.
func (MiniMax) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	model, voiceID := voice(r, "speech-2.8-hd", "English_expressive_narrator", r.Model)
	req := map[string]any{
		"model": model, "text": body(r).Get("input").String(), "stream": false,
		"language_boost": "auto", "output_format": "hex",
		"voice_setting": map[string]any{"voice_id": voiceID, "speed": 1, "vol": 1, "pitch": 0},
		"audio_setting": map[string]any{"sample_rate": 32000, "bitrate": 128000, "format": "mp3", "channel": 1},
	}
	raw, err := jsonBody(req)
	if err != nil {
		return media.Call{}, err
	}
	base := media.BaseURL(r)
	if base == "" {
		base = "https://api.minimax.io"
	}
	base = strings.TrimSuffix(base, "/t2a_v2")
	return media.Call{Method: http.MethodPost, URL: base + "/t2a_v2", Header: headers(r), Body: raw}, nil
}

// Parse reads the hex audio, refusing a business error.
func (MiniMax) Parse(_ context.Context, _ media.Request, _ media.Client, status int, _ http.Header, body []byte) (media.Response, error) {
	if status < 200 || status > 299 {
		return media.Response{}, &media.ProviderError{Status: status, Text: "the provider refused the speech"}
	}
	if gjson.GetBytes(body, "base_resp.status_code").Int() != 0 {
		return media.Response{}, &media.ProviderError{Status: http.StatusBadGateway, Text: "the provider refused the speech"}
	}
	audio, err := hex.DecodeString(gjson.GetBytes(body, "data.audio").String())
	if err != nil || len(audio) < 100 {
		return media.Response{}, &media.ProviderError{Status: http.StatusBadGateway, Text: "the provider returned no audio"}
	}
	format := gjson.GetBytes(body, "extra_info.audio_format").String()
	if format == "" {
		format = "mp3"
	}
	return media.Response{Status: http.StatusOK, Header: map[string]string{"Content-Type": media.AudioMIME(format)}, Body: audio}, nil
}
