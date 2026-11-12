// Package tts serves the speech APIs: each provider's body, headers and audio
// format, behind the one adapter interface.
package tts

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
)

// kind is every adapter's service.
func kind() string { return upstream.KindTTS }

// knownVoiceModels lists the models voices are parsed against, per config.
func knownVoiceModels(r media.Request) []string {
	var out []string
	if r.Config != nil {
		for _, m := range r.Config.Models {
			out = append(out, m.ID)
		}
	}
	for _, m := range r.Entry.Models {
		if m.Kind == upstream.KindTTS {
			out = append(out, m.ID)
		}
	}
	return out
}

// body is the client's JSON request.
func body(r media.Request) gjson.Result { return gjson.ParseBytes(r.Body) }

// voice is the request's voice, from the body's own field when the client sent
// one, else from the model name.
func voice(r media.Request, defModel, defVoice, rest string) (model, voiceID string) {
	model, voiceID = media.ParseModelVoice(rest, defModel, defVoice, knownVoiceModels(r))
	if v := body(r).Get("voice").String(); v != "" {
		voiceID = v
	}
	return model, voiceID
}

// base is the provider's speech address, with a trailing path or /v1 removed.
func base(cfg *upstream.MediaConfig, account, fallback string) string {
	b := ""
	if cfg != nil {
		b = cfg.BaseURL
	}
	if b == "" {
		b = account
	}
	if b == "" {
		b = fallback
	}
	b = strings.TrimRight(b, "/")
	b = strings.TrimSuffix(b, "/v1/audio/speech")
	b = strings.TrimSuffix(b, "/v1")
	return b
}

// audioAnswer is the provider's bytes as the media response.
func audioAnswer(status int, header http.Header, audio []byte, fallback string) (media.Response, error) {
	if status < 200 || status > 299 {
		return media.Response{}, &media.ProviderError{Status: status, Text: "the provider refused the speech"}
	}
	if len(audio) < 100 {
		return media.Response{}, &media.ProviderError{Status: http.StatusBadGateway, Text: "the provider returned no audio"}
	}
	format := media.AudioFormat(header.Get("Content-Type"), fallback)
	return media.Response{Status: http.StatusOK, Header: map[string]string{
		"Content-Type": media.AudioMIME(format),
	}, Body: audio}, nil
}

func jsonAudio(b64, format string) (media.Response, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(raw) < 100 {
		return media.Response{}, &media.ProviderError{Status: http.StatusBadGateway, Text: "the provider returned no audio"}
	}
	return media.Response{Status: http.StatusOK, Header: map[string]string{
		"Content-Type": media.AudioMIME(format),
	}, Body: raw}, nil
}

// jsonBody marshals a request body.
func jsonBody(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, media.BadRequestf("the request cannot be built")
	}
	return raw, nil
}

// headers is the provider's static headers plus a bearer key when one exists.
func headers(r media.Request) map[string]string {
	h := map[string]string{"Content-Type": "application/json"}
	if r.Config != nil {
		for k, v := range r.Config.Headers {
			h[k] = v
		}
	}
	if r.Credential != "" {
		h["Authorization"] = "Bearer " + r.Credential
	}
	return h
}
