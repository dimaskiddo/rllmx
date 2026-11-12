package tts

import (
	"context"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
)

// Cartesia speaks its bytes endpoint.
type Cartesia struct{}

// Kind is the service it serves.
func (Cartesia) Kind() string { return kind() }

// Build makes the call.
func (Cartesia) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	model, voiceID := voice(r, "", "", r.Model)
	req := map[string]any{
		"model_id": model, "transcript": body(r).Get("input").String(),
		"output_format": map[string]any{"container": "mp3", "bit_rate": 128000, "sample_rate": 44100},
	}
	if voiceID != "" {
		req["voice"] = map[string]string{"mode": "id", "id": voiceID}
	}
	raw, err := jsonBody(req)
	if err != nil {
		return media.Call{}, err
	}
	h := map[string]string{"Content-Type": "application/json", "Cartesia-Version": "2024-06-10"}
	if r.Credential != "" {
		h["X-API-Key"] = r.Credential
	}
	return media.Call{Method: http.MethodPost, URL: media.BaseURL(r), Header: h, Body: raw}, nil
}

// Parse passes the audio through.
func (Cartesia) Parse(_ context.Context, _ media.Request, _ media.Client, status int, header http.Header, audio []byte) (media.Response, error) {
	return audioAnswer(status, header, audio, "mp3")
}

// Inworld answers base64 audio in JSON.
type Inworld struct{}

// Kind is the service it serves.
func (Inworld) Kind() string { return kind() }

// Build makes the call.
func (Inworld) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	model, voiceID := voice(r, "inworld-tts-1.5-mini", "Alex", r.Model)
	raw, err := jsonBody(map[string]any{
		"text": body(r).Get("input").String(), "voiceId": voiceID, "modelId": model,
		"audioConfig": map[string]string{"audioEncoding": "MP3"},
	})
	if err != nil {
		return media.Call{}, err
	}
	h := map[string]string{"Content-Type": "application/json"}
	if r.Credential != "" {
		h["Authorization"] = "Basic " + r.Credential
	}
	return media.Call{Method: http.MethodPost, URL: media.BaseURL(r), Header: h, Body: raw}, nil
}

// Parse reads the base64 audio.
func (Inworld) Parse(_ context.Context, _ media.Request, _ media.Client, status int, _ http.Header, body []byte) (media.Response, error) {
	if status < 200 || status > 299 {
		return media.Response{}, &media.ProviderError{Status: status, Text: "the provider refused the speech"}
	}
	return jsonAudio(gjson.GetBytes(body, "audioContent").String(), "mp3")
}

// PlayHT splits its credential into a user id and a key.
type PlayHT struct{}

// Kind is the service it serves.
func (PlayHT) Kind() string { return kind() }

// Build makes the call.
func (PlayHT) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	model, voiceID := voice(r, "PlayDialog", "s3://voice-cloning-zero-shot/d9ff78ba-d016-47f6-b0ef-dd630f59414e/female-cs/manifest.json", r.Model)
	raw, err := jsonBody(map[string]any{
		"text": body(r).Get("input").String(), "voice": voiceID, "voice_engine": model,
		"output_format": "mp3", "speed": 1,
	})
	if err != nil {
		return media.Call{}, err
	}
	user, key, _ := strings.Cut(r.Credential, ":")
	h := map[string]string{"Content-Type": "application/json", "Accept": "audio/mpeg"}
	if user != "" {
		h["X-USER-ID"] = user
	}
	if key != "" {
		h["Authorization"] = "Bearer " + key
	}
	return media.Call{Method: http.MethodPost, URL: media.BaseURL(r), Header: h, Body: raw}, nil
}

// Parse passes the audio through.
func (PlayHT) Parse(_ context.Context, _ media.Request, _ media.Client, status int, header http.Header, audio []byte) (media.Response, error) {
	return audioAnswer(status, header, audio, "mp3")
}

// FishAudio speaks with the model in a header.
type FishAudio struct{}

// Kind is the service it serves.
func (FishAudio) Kind() string { return kind() }

// Build makes the call.
func (FishAudio) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	model, voiceID := voice(r, "s2.1-pro-free", "", r.Model)
	req := map[string]any{"text": body(r).Get("input").String(), "format": "mp3"}
	if voiceID != "" {
		req["reference_id"] = voiceID
	}
	raw, err := jsonBody(req)
	if err != nil {
		return media.Call{}, err
	}
	h := headers(r)
	h["model"] = model
	return media.Call{Method: http.MethodPost, URL: media.BaseURL(r), Header: h, Body: raw}, nil
}

// Parse passes the audio through.
func (FishAudio) Parse(_ context.Context, _ media.Request, _ media.Client, status int, header http.Header, audio []byte) (media.Response, error) {
	return audioAnswer(status, header, audio, "mp3")
}

// NVIDIA speaks its speech endpoint.
type NVIDIA struct{}

// Kind is the service it serves.
func (NVIDIA) Kind() string { return kind() }

// Build makes the call.
func (NVIDIA) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	model, voiceID := voice(r, "", "", r.Model)
	if voiceID == "" {
		voiceID = "default"
	}
	raw, err := jsonBody(map[string]any{
		"input": map[string]string{"text": body(r).Get("input").String()},
		"voice": voiceID, "model": model,
	})
	if err != nil {
		return media.Call{}, err
	}
	return media.Call{Method: http.MethodPost, URL: media.BaseURL(r), Header: headers(r), Body: raw}, nil
}

// Parse passes the audio through.
func (NVIDIA) Parse(_ context.Context, _ media.Request, _ media.Client, status int, header http.Header, audio []byte) (media.Response, error) {
	return audioAnswer(status, header, audio, "wav")
}

// Coqui speaks the local server's JSON.
type Coqui struct{}

// Kind is the service it serves.
func (Coqui) Kind() string { return kind() }

// Build makes the call.
func (Coqui) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	payload := map[string]any{"text": body(r).Get("input").String()}
	if _, voiceID := voice(r, "", "", r.Model); voiceID != "" {
		payload["speaker_id"] = voiceID
	}
	raw, err := jsonBody(payload)
	if err != nil {
		return media.Call{}, err
	}
	return media.Call{Method: http.MethodPost, URL: media.BaseURL(r), Header: map[string]string{"Content-Type": "application/json"}, Body: raw}, nil
}

// Parse passes the audio through.
func (Coqui) Parse(_ context.Context, _ media.Request, _ media.Client, status int, header http.Header, audio []byte) (media.Response, error) {
	return audioAnswer(status, header, audio, "wav")
}

// Tortoise speaks the local server's JSON.
type Tortoise struct{}

// Kind is the service it serves.
func (Tortoise) Kind() string { return kind() }

// Build makes the call.
func (Tortoise) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	_, voiceID := voice(r, "", "", r.Model)
	if voiceID == "" {
		voiceID = "random"
	}
	raw, err := jsonBody(map[string]any{"text": body(r).Get("input").String(), "voice": voiceID})
	if err != nil {
		return media.Call{}, err
	}
	return media.Call{Method: http.MethodPost, URL: media.BaseURL(r), Header: map[string]string{"Content-Type": "application/json"}, Body: raw}, nil
}

// Parse passes the audio through.
func (Tortoise) Parse(_ context.Context, _ media.Request, _ media.Client, status int, header http.Header, audio []byte) (media.Response, error) {
	return audioAnswer(status, header, audio, "wav")
}
