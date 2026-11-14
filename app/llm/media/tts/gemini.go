package tts

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"net/http"
	"net/url"
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
)

// Gemini asks Gemini for an AUDIO answer and wraps its PCM in a WAV header.
type Gemini struct{}

// Kind is the service it serves.
func (Gemini) Kind() string { return kind() }

// Build makes the call.
func (Gemini) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	model, voiceID := voice(r, "gemini-3.1-flash-tts-preview", "Kore", r.Model)
	prompt := strings.TrimSpace(body(r).Get("input").String())
	if i := strings.Index(prompt, ": "); i < 0 {
		prompt = "Say: " + prompt
	}
	req := map[string]any{
		"contents": []map[string]any{{"parts": []map[string]string{{"text": prompt}}}},
		"generationConfig": map[string]any{
			"responseModalities": []string{"AUDIO"},
			"speechConfig": map[string]any{
				"voiceConfig": map[string]any{"prebuiltVoiceConfig": map[string]string{"voiceName": voiceID}},
			},
		},
	}
	raw, err := jsonBody(req)
	if err != nil {
		return media.Call{}, err
	}
	base := media.BaseURL(r)
	if base == "" {
		base = "https://generativelanguage.googleapis.com/v1beta/models"
	}
	q := url.Values{}
	if r.Credential != "" {
		q.Set("key", r.Credential)
	}
	target := base + "/" + url.PathEscape(model) + ":generateContent"
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	return media.Call{Method: http.MethodPost, URL: target, Header: map[string]string{"Content-Type": "application/json"}, Body: raw}, nil
}

// Parse reads the PCM part and wraps it as a WAV file.
func (Gemini) Parse(_ context.Context, _ media.Request, _ media.Client, status int, _ http.Header, body []byte) (media.Response, error) {
	if status < 200 || status > 299 {
		return media.Response{}, &media.ProviderError{Status: status, Text: "the provider refused the speech"}
	}
	for _, part := range gjson.GetBytes(body, "candidates.0.content.parts").Array() {
		if data := part.Get("inlineData.data").String(); data != "" {
			pcm, err := base64.StdEncoding.DecodeString(data)
			if err != nil || len(pcm) < 100 {
				return media.Response{}, &media.ProviderError{Status: http.StatusBadGateway, Text: "the provider returned no audio"}
			}
			return media.Response{Status: http.StatusOK, Header: map[string]string{"Content-Type": media.AudioMIME("wav")}, Body: wav(pcm)}, nil
		}
	}
	return media.Response{}, &media.ProviderError{Status: http.StatusBadGateway, Text: "the provider returned no audio"}
}

// wav wraps 24 kHz mono 16-bit PCM in a 44-byte header.
func wav(pcm []byte) []byte {
	const rate = 24000
	out := make([]byte, 44+len(pcm))
	copy(out[0:4], "RIFF")
	binary.LittleEndian.PutUint32(out[4:8], uint32(36+len(pcm)))
	copy(out[8:12], "WAVE")
	copy(out[12:16], "fmt ")
	binary.LittleEndian.PutUint32(out[16:20], 16)
	binary.LittleEndian.PutUint16(out[20:22], 1)
	binary.LittleEndian.PutUint16(out[22:24], 1)
	binary.LittleEndian.PutUint32(out[24:28], rate)
	binary.LittleEndian.PutUint32(out[28:32], rate*2)
	binary.LittleEndian.PutUint16(out[32:34], 2)
	binary.LittleEndian.PutUint16(out[34:36], 16)
	copy(out[36:40], "data")
	binary.LittleEndian.PutUint32(out[40:44], uint32(len(pcm)))
	copy(out[44:], pcm)
	return out
}
