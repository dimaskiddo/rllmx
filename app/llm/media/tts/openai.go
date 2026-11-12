package tts

import (
	"context"
	"net/http"
	"strings"

	"rllmx/app/llm/media"
)

// OpenAI speaks the OpenAI speech body at the provider's address.
type OpenAI struct{}

// Kind is the service it serves.
func (OpenAI) Kind() string { return kind() }

// Build makes the call; the model and voice come from the name.
func (OpenAI) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	model, voiceID := openAIVoice(r)
	req := map[string]any{"model": model, "voice": voiceID, "input": body(r).Get("input").String()}
	raw, err := jsonBody(req)
	if err != nil {
		return media.Call{}, err
	}
	target := base(r.Config, r.Account["baseUrl"], "https://api.openai.com") + "/v1/audio/speech"
	return media.Call{Method: http.MethodPost, URL: target, Header: headers(r), Body: raw}, nil
}

// openAIVoice splits the name the way the reference does: a model then a voice,
// a bare known model, or a bare voice with the provider's default model.
func openAIVoice(r media.Request) (model, voiceID string) {
	defModel := "gpt-4o-mini-tts"
	if r.Config != nil && r.Config.DefaultModel != "" {
		defModel = r.Config.DefaultModel
	}
	name := r.Model
	if name == "" {
		return defModel, "alloy"
	}
	if before, after, ok := strings.Cut(name, "/"); ok {
		return before, after
	}
	for _, m := range knownVoiceModels(r) {
		if m == name {
			return name, "alloy"
		}
	}
	if v := body(r).Get("voice").String(); v != "" {
		return defModel, v
	}
	return defModel, name
}

// Parse passes the audio bytes through.
func (OpenAI) Parse(_ context.Context, _ media.Request, _ media.Client, status int, header http.Header, audio []byte) (media.Response, error) {
	return audioAnswer(status, header, audio, "mp3")
}

// LocalOpenAI speaks the same body at a self-hosted address, where the account's
// own base URL wins and the requested format goes upstream.
type LocalOpenAI struct{}

// Kind is the service it serves.
func (LocalOpenAI) Kind() string { return kind() }

// Build makes the call.
func (LocalOpenAI) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	defModel, defVoice := "kokoro", "af_heart"
	if r.Provider == "ollama-local" {
		defModel, defVoice = "phonon:multilingual", "default"
	}
	model, voiceID := voice(r, defModel, defVoice, r.Model)
	format := strings.ToLower(r.Config.Formats[0])
	if f := body(r).Get("response_format").String(); f != "" && f != "json" {
		format = strings.ToLower(f)
	}
	if format == "" || format == "json" {
		format = "mp3"
	}
	req := map[string]any{"model": model, "voice": voiceID, "input": body(r).Get("input").String()}
	if r.Provider != "ollama-local" {
		req["response_format"] = format
	} else {
		upstreamFormat := "wav"
		if format == "pcm" {
			upstreamFormat = "pcm"
		}
		req["response_format"] = upstreamFormat
		format = upstreamFormat
	}
	raw, err := jsonBody(req)
	if err != nil {
		return media.Call{}, err
	}
	h := map[string]string{"Content-Type": "application/json"}
	if r.Credential != "" {
		h["Authorization"] = "Bearer " + r.Credential
	}
	target := base(r.Config, r.Account["baseUrl"], "http://localhost:8880") + "/v1/audio/speech"
	return media.Call{Method: http.MethodPost, URL: target, Header: h, Body: raw}, nil
}

// Parse passes the audio through, named by the requested format.
func (LocalOpenAI) Parse(_ context.Context, r media.Request, _ media.Client, status int, header http.Header, audio []byte) (media.Response, error) {
	fallback := "mp3"
	if r.Provider == "ollama-local" {
		fallback = "wav"
	}
	if f := body(r).Get("response_format").String(); f != "" && f != "json" {
		fallback = strings.ToLower(f)
	}
	if fallback == "pcm" {
		fallback = "pcm"
	}
	return audioAnswer(status, header, audio, fallback)
}
