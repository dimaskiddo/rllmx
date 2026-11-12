// Package voice lists the voices a speech provider offers live: a provider
// whose own API is asked, rather than a table in its data file.
package voice

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
)

// List asks one provider for its voices.
func List(ctx context.Context, provider string, r media.Request, c media.Client) ([]upstream.Voice, error) {
	switch provider {
	case "elevenlabs":
		return elevenlabs(ctx, r, c)
	case "inworld":
		return inworld(ctx, r, c)
	case "deepgram":
		return deepgram(ctx, r, c)
	case "minimax", "minimax-cn":
		return minimax(ctx, r, c)
	}
	return nil, nil
}

func elevenlabs(ctx context.Context, r media.Request, c media.Client) ([]upstream.Voice, error) {
	status, _, body, err := c.Do(ctx, http.MethodGet, "https://api.elevenlabs.io/v1/voices", map[string]string{"xi-api-key": r.Credential}, nil)
	if err != nil || status < 200 || status > 299 {
		return nil, &media.ProviderError{Status: statusOr(status), Text: "the provider refused the voice list"}
	}
	var out []upstream.Voice
	for _, v := range gjson.GetBytes(body, "voices").Array() {
		out = append(out, upstream.Voice{
			ID: v.Get("voice_id").String(), Name: v.Get("name").String(),
			Lang: v.Get("labels.language").String(), Gender: v.Get("labels.gender").String(),
		})
	}
	return out, nil
}

func inworld(ctx context.Context, r media.Request, c media.Client) ([]upstream.Voice, error) {
	status, _, body, err := c.Do(ctx, http.MethodGet, "https://api.inworld.ai/tts/v1/voices",
		map[string]string{"Authorization": "Basic " + r.Credential}, nil)
	if err != nil || status < 200 || status > 299 {
		return nil, &media.ProviderError{Status: statusOr(status), Text: "the provider refused the voice list"}
	}
	var out []upstream.Voice
	for _, v := range gjson.GetBytes(body, "voices").Array() {
		out = append(out, upstream.Voice{ID: v.Get("voiceId").String(), Name: v.Get("displayName").String(), Lang: v.Get("languages.0").String()})
	}
	return out, nil
}

func deepgram(ctx context.Context, r media.Request, c media.Client) ([]upstream.Voice, error) {
	status, _, body, err := c.Do(ctx, http.MethodGet, "https://api.deepgram.com/v1/models",
		map[string]string{"Authorization": "Token " + r.Credential}, nil)
	if err != nil || status < 200 || status > 299 {
		return nil, &media.ProviderError{Status: statusOr(status), Text: "the provider refused the voice list"}
	}
	var out []upstream.Voice
	for _, v := range gjson.GetBytes(body, "tts").Array() {
		out = append(out, upstream.Voice{ID: v.Get("canonical_name").String(), Name: v.Get("name").String(), Lang: v.Get("language").String()})
	}
	return out, nil
}

func minimax(ctx context.Context, r media.Request, c media.Client) ([]upstream.Voice, error) {
	base := "https://api.minimax.io"
	if r.Provider == "minimax-cn" {
		base = "https://api.minimaxi.com"
	}
	payload, _ := json.Marshal(map[string]string{"voice_type": "all"})
	status, _, body, err := c.Do(ctx, http.MethodPost, base+"/v1/get_voice",
		map[string]string{"Authorization": "Bearer " + r.Credential, "Content-Type": "application/json"}, payload)
	if err != nil || status < 200 || status > 299 {
		return nil, &media.ProviderError{Status: statusOr(status), Text: "the provider refused the voice list"}
	}
	var out []upstream.Voice
	for _, v := range gjson.GetBytes(body, "system_voice").Array() {
		out = append(out, upstream.Voice{ID: v.Get("voice_id").String(), Name: v.Get("voice_name").String()})
	}
	return out, nil
}

func statusOr(status int) int {
	if status == 0 {
		return http.StatusBadGateway
	}
	return status
}
