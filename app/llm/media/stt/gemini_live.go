package stt

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
	"rllmx/app/platform/network"
	"rllmx/app/protocol/websocket"
)

const (
	liveChunk    = 16384
	liveSetup    = 10 * time.Second
	liveTurn     = 60 * time.Second
	liveMaxTimer = 300 * time.Second
)

// GeminiLive streams the audio to Gemini's bidi endpoint over a WebSocket.
type GeminiLive struct{}

// Kind is the service it serves.
func (GeminiLive) Kind() string { return kind() }

// Direct runs the WebSocket exchange.
func (GeminiLive) Direct(ctx context.Context, r media.Request, _ media.Client) (media.Response, error) {
	if r.Upload == nil || len(r.Upload.Audio) == 0 {
		return media.Response{}, media.BadRequestf("the request names no file")
	}
	target, err := liveURL(r)
	if err != nil {
		return media.Response{}, err
	}
	model := field(r, "model")
	if model == "" {
		model = r.Model
	}
	prompt := livePrompt(r)
	setup, _ := json.Marshal(map[string]any{"setup": map[string]any{
		"model": "models/" + strings.TrimPrefix(model, "models/"),
		"generationConfig": map[string]any{
			"responseModalities": []string{"TEXT"}, "inputAudioTranscription": map[string]any{},
		},
		"systemInstruction": map[string]any{"parts": []map[string]string{{"text": prompt}}},
	}})
	text, err := liveRun(ctx, r, target, setup)
	if err != nil {
		return media.Response{}, err
	}
	return liveAnswer(r, text)
}

// liveURL is the bidi address from the provider's base and the model.
func liveURL(r media.Request) (string, error) {
	base := media.BaseURL(r)
	if base == "" {
		return "", media.BadRequestf("the provider names no address")
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return "", media.BadRequestf("the provider names no address")
	}
	model := field(r, "model")
	if model == "" {
		model = r.Model
	}
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	path := "/ws/api" + u.Path
	u.Path = strings.TrimRight(path, "/") + "/" + url.PathEscape(model) + ":bidiGenerateContent"
	q := u.Query()
	if r.Credential != "" {
		q.Set("key", r.Credential)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// livePrompt is the instruction: the caller's own, else the reference default.
func livePrompt(r media.Request) string {
	if v := field(r, "system_instruction"); v != "" {
		return v
	}
	prompt := field(r, "prompt")
	if prompt == "" {
		prompt = "Transcribe the spoken audio verbatim."
	}
	if language := field(r, "language"); language != "" {
		prompt += " Language: " + language + "."
	}
	return prompt
}

func liveRun(ctx context.Context, r media.Request, target string, setup []byte) (string, error) {
	guard := network.NewDialer(r.Network.ConnectTimeout, r.Network.Allow)
	conn, err := websocket.Dial(ctx, target, nil, websocket.Options{
		Dial: guard.DialContext, TLSConfig: network.NewTransport(r.Network).TLSClientConfig, Timeout: r.Network.ConnectTimeout,
	})
	if err != nil {
		return "", &media.ProviderError{Status: http.StatusBadGateway, Text: "the provider could not be reached"}
	}
	defer conn.Close()
	setupWait := liveTimer(r, "setup_timeout_ms", liveSetup)
	turnWait := liveTimer(r, "turn_timeout_ms", liveTurn)
	_ = conn.SetDeadline(time.Now().Add(setupWait))
	if err := conn.WriteText(setup); err != nil {
		return "", &media.ProviderError{Status: http.StatusBadGateway, Text: "the stream failed"}
	}
	var text strings.Builder
	ready := false
	for !ready {
		frame, err := conn.ReadText()
		if err != nil {
			return "", &media.ProviderError{Status: http.StatusGatewayTimeout, Text: "the provider did not set up the stream"}
		}
		text.WriteString(gjson.GetBytes(frame, "serverContent.inputTranscription.text").String())
		if gjson.GetBytes(frame, "serverContent.setupComplete").Exists() || gjson.GetBytes(frame, "setupComplete").Exists() {
			ready = true
		}
		if msg := gjson.GetBytes(frame, "error.message").String(); msg != "" {
			return "", &media.ProviderError{Status: http.StatusBadGateway, Text: "the provider refused the stream"}
		}
	}
	audio := r.Upload.Audio
	for off := 0; off < len(audio); off += liveChunk {
		end := off + liveChunk
		if end > len(audio) {
			end = len(audio)
		}
		chunk, _ := json.Marshal(map[string]any{"realtimeInput": map[string]any{
			"mediaChunks": []map[string]string{{"mimeType": audioMIME(r), "data": base64.StdEncoding.EncodeToString(audio[off:end])}},
		}})
		if err := conn.WriteText(chunk); err != nil {
			return livePartial(text.String())
		}
	}
	turn, _ := json.Marshal(map[string]any{"clientContent": map[string]any{
		"turns": []map[string]any{{"parts": []map[string]string{{"text": livePrompt(r)}}}}, "turnComplete": true,
	}})
	if err := conn.WriteText(turn); err != nil {
		return livePartial(text.String())
	}
	_ = conn.SetDeadline(time.Now().Add(turnWait))
	for {
		frame, err := conn.ReadText()
		if err != nil {
			return livePartial(text.String())
		}
		text.WriteString(gjson.GetBytes(frame, "serverContent.inputTranscription.text").String())
		if msg := gjson.GetBytes(frame, "error.message").String(); msg != "" {
			return "", &media.ProviderError{Status: http.StatusBadGateway, Text: "the provider refused the stream"}
		}
		if gjson.GetBytes(frame, "serverContent.turnComplete").Exists() || gjson.GetBytes(frame, "goAway").Exists() {
			if text.Len() > 0 {
				return text.String(), nil
			}
			return "", &media.ProviderError{Status: http.StatusBadGateway, Text: "the provider answered no transcript"}
		}
	}
}

func liveTimer(r media.Request, name string, fallback time.Duration) time.Duration {
	v := field(r, name)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return fallback
	}
	d := time.Duration(n) * time.Millisecond
	if d > liveMaxTimer {
		return liveMaxTimer
	}
	return d
}

func livePartial(text string) (string, error) {
	if strings.TrimSpace(text) == "" {
		return "", &media.ProviderError{Status: http.StatusGatewayTimeout, Text: "the provider did not finish transcribing"}
	}
	return text, nil
}

func liveAnswer(r media.Request, text string) (media.Response, error) {
	if field(r, "response_format") == "verbose_json" {
		raw, _ := json.Marshal(map[string]any{
			"text": text, "segments": []map[string]string{{"id": "0", "text": text}},
		})
		return media.Response{Status: http.StatusOK, Header: map[string]string{"Content-Type": "application/json"}, Body: raw}, nil
	}
	raw, _ := json.Marshal(map[string]string{"text": text})
	return media.Response{Status: http.StatusOK, Header: map[string]string{"Content-Type": "application/json"}, Body: raw}, nil
}

// Build is unused: Direct runs the whole exchange.
func (GeminiLive) Build(context.Context, media.Request, media.Client) (media.Call, error) {
	return media.Call{}, media.BadRequestf("this adapter runs its own exchange")
}

// Parse is unused.
func (GeminiLive) Parse(context.Context, media.Request, media.Client, int, http.Header, []byte) (media.Response, error) {
	return media.Response{}, media.BadRequestf("this adapter runs its own exchange")
}
