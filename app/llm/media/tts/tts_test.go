package tts

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
)

func req(provider, model, body string, cfg *upstream.MediaConfig) media.Request {
	return media.Request{Kind: upstream.KindTTS, Provider: provider, Model: model, WireModel: model,
		Config: cfg, Credential: "KEY", Body: []byte(body), Account: map[string]string{}}
}

func TestOpenAISpeaksTheSpeechBody(t *testing.T) {
	cfg := &upstream.MediaConfig{BaseURL: "https://api.openai.com/v1/audio/speech"}
	call, err := (OpenAI{}).Build(context.Background(), req("openai", "tts-1/nova", `{"input":"hello"}`, cfg), nil)
	if err != nil {
		t.Fatal(err)
	}
	if call.URL != "https://api.openai.com/v1/audio/speech" || call.Header["Authorization"] != "Bearer KEY" {
		t.Fatalf("%s %v", call.URL, call.Header)
	}
	if gjson.GetBytes(call.Body, "model").String() != "tts-1" || gjson.GetBytes(call.Body, "voice").String() != "nova" {
		t.Fatalf("%s", call.Body)
	}
}

func TestABareVoiceUsesTheDefaultModel(t *testing.T) {
	cfg := &upstream.MediaConfig{BaseURL: "https://api.openai.com", DefaultModel: "gpt-4o-mini-tts"}
	call, err := (OpenAI{}).Build(context.Background(), req("openai", "shimmer", `{"input":"hi"}`, cfg), nil)
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(call.Body, "model").String() != "gpt-4o-mini-tts" || gjson.GetBytes(call.Body, "voice").String() != "shimmer" {
		t.Fatalf("%s", call.Body)
	}
}

func TestMiniMaxReadsHexAudio(t *testing.T) {
	cfg := &upstream.MediaConfig{BaseURL: "https://api.minimax.io/v1/t2a_v2"}
	call, err := (MiniMax{}).Build(context.Background(), req("minimax", "speech-2.8-hd/English_expressive_narrator", `{"input":"hi"}`, cfg), nil)
	if err != nil || !strings.HasSuffix(call.URL, "/t2a_v2") {
		t.Fatalf("%s %v", call.URL, err)
	}
	audio := strings.Repeat("ab", 100)
	body, _ := json.Marshal(map[string]any{"data": map[string]string{"audio": hex.EncodeToString([]byte(audio))}, "extra_info": map[string]string{"audio_format": "mp3"}})
	out, err := (MiniMax{}).Parse(context.Background(), media.Request{}, nil, 200, nil, body)
	if err != nil || len(out.Body) != 200 || out.Header["Content-Type"] != "audio/mpeg" {
		t.Fatalf("%d %v", len(out.Body), err)
	}
}

func TestGeminiWrapsPCM(t *testing.T) {
	cfg := &upstream.MediaConfig{BaseURL: "https://generativelanguage.googleapis.com/v1beta/models"}
	call, err := (Gemini{}).Build(context.Background(), req("gemini", "gemini-3.1-flash-tts-preview/Kore", `{"input":"hi"}`, cfg), nil)
	if err != nil || !strings.Contains(call.URL, ":generateContent?key=KEY") {
		t.Fatalf("%s %v", call.URL, err)
	}
	pcm := strings.Repeat("p", 200)
	body, _ := json.Marshal(map[string]any{
		"candidates": []map[string]any{{
			"content": map[string]any{"parts": []map[string]any{{"inlineData": map[string]string{"data": b64(pcm)}}}},
		}},
	})
	out, err := (Gemini{}).Parse(context.Background(), media.Request{}, nil, 200, nil, body)
	if err != nil || len(out.Body) != len(pcm)+44 || string(out.Body[:4]) != "RIFF" {
		t.Fatalf("%d %v", len(out.Body), err)
	}
}

func TestElevenLabsUsesTheVoiceInThePath(t *testing.T) {
	cfg := &upstream.MediaConfig{BaseURL: "https://api.elevenlabs.io/v1/text-to-speech"}
	call, err := (ElevenLabs{}).Build(context.Background(), req("elevenlabs", "eleven_multilingual_v2/abc123", `{"input":"hi"}`, cfg), nil)
	if err != nil || !strings.HasSuffix(call.URL, "/abc123") || call.Header["xi-api-key"] != "KEY" {
		t.Fatalf("%s %v", call.URL, err)
	}
}

func TestAShortAnswerIsRefused(t *testing.T) {
	if _, err := (OpenAI{}).Parse(context.Background(), media.Request{}, nil, 200, nil, []byte("tiny")); err == nil {
		t.Fatal("accepted")
	}
}

func b64(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}
