package tts

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
)

// Xiaomi speaks MiMo through its chat-completions audio mode.
type Xiaomi struct{}

// Kind is the service it serves.
func (Xiaomi) Kind() string { return kind() }

// Build makes the chat call.
func (Xiaomi) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	model, voiceID := voice(r, "mimo-v2.5-tts", "mimo_default", r.Model)
	req := body(r)
	messages := []map[string]string{}
	if language := req.Get("language").String(); language != "" || req.Get("style").String() != "" {
		hint := "Speak in " + language + ". "
		if language == "" {
			hint = ""
		}
		messages = append(messages, map[string]string{"role": "user", "content": hint + req.Get("style").String()})
	}
	messages = append(messages, map[string]string{"role": "assistant", "content": req.Get("input").String()})
	payload, err := jsonBody(map[string]any{
		"model": model, "stream": false, "messages": messages,
		"audio": map[string]string{"format": "wav", "voice": voiceID},
	})
	if err != nil {
		return media.Call{}, err
	}
	base := strings.TrimRight(media.BaseURL(r), "/")
	if base == "" {
		base = "https://api.xiaomimimo.com/v1/chat/completions"
	}
	if !strings.HasSuffix(base, "/chat/completions") {
		base += "/chat/completions"
	}
	return media.Call{Method: http.MethodPost, URL: base, Header: headers(r), Body: payload}, nil
}

// Parse reads the base64 audio of the message.
func (Xiaomi) Parse(_ context.Context, _ media.Request, _ media.Client, status int, _ http.Header, body []byte) (media.Response, error) {
	if status < 200 || status > 299 {
		return media.Response{}, &media.ProviderError{Status: status, Text: "the provider refused the speech"}
	}
	format := gjson.GetBytes(body, "choices.0.message.audio.format").String()
	if format == "" {
		format = "wav"
	}
	return jsonAudio(gjson.GetBytes(body, "choices.0.message.audio.data").String(), format)
}

// OpenRouter speaks through chat completions in streamed audio chunks.
type OpenRouter struct{}

// Kind is the service it serves.
func (OpenRouter) Kind() string { return kind() }

// Build makes the streaming chat call.
func (OpenRouter) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	model, voiceID := openRouterVoice(r)
	payload, err := jsonBody(map[string]any{
		"model": model, "modalities": []string{"text", "audio"},
		"audio":    map[string]string{"voice": voiceID, "format": "wav"},
		"stream":   true,
		"messages": []map[string]string{{"role": "user", "content": body(r).Get("input").String()}},
	})
	if err != nil {
		return media.Call{}, err
	}
	return media.Call{Method: http.MethodPost, URL: media.BaseURL(r), Header: headers(r), Body: payload}, nil
}

// Parse joins the audio chunks of the SSE answer.
func (OpenRouter) Parse(_ context.Context, _ media.Request, _ media.Client, status int, _ http.Header, body []byte) (media.Response, error) {
	if status < 200 || status > 299 {
		return media.Response{}, &media.ProviderError{Status: status, Text: "the provider refused the speech"}
	}
	var joined strings.Builder
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if line == "" || line == "[DONE]" {
			continue
		}
		joined.WriteString(gjson.Get(line, "choices.0.delta.audio.data").String())
	}
	if joined.Len() == 0 {
		return media.Response{}, &media.ProviderError{Status: http.StatusBadGateway, Text: "the provider returned no audio"}
	}
	audio, err := base64.StdEncoding.DecodeString(joined.String())
	if err != nil {
		return media.Response{}, &media.ProviderError{Status: http.StatusBadGateway, Text: "the provider returned no audio"}
	}
	return audioAnswer(http.StatusOK, nil, audio, "wav")
}

// openRouterVoice splits the last slash: a left half that itself holds a slash
// is a model; anything else is the voice.
func openRouterVoice(r media.Request) (model, voiceID string) {
	name := r.Model
	idx := strings.LastIndexByte(name, '/')
	if idx > 0 && strings.Contains(name[:idx], "/") {
		return name[:idx], name[idx+1:]
	}
	if v := body(r).Get("voice").String(); v != "" {
		return "openai/gpt-4o-mini-tts", v
	}
	if name != "" {
		return "openai/gpt-4o-mini-tts", name
	}
	return "openai/gpt-4o-mini-tts", "alloy"
}
