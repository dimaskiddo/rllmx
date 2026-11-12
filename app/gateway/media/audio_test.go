package media_test

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/gateway/account"
	gatewaymedia "rllmx/app/gateway/media"
	mediamux "rllmx/app/llm/media/mux"
	"rllmx/app/llm/upstream"
	"rllmx/app/store/repository"
	"rllmx/tests"
	"rllmx/tests/store"
	"rllmx/tests/usage"
)

func audioRig(t *testing.T, entry string, answer func(w http.ResponseWriter, r *http.Request)) (*gatewaymedia.Pipeline, *usage.Sink) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(answer))
	t.Cleanup(srv.Close)
	reg, err := upstream.Load(fstest.MapFS{"data/openai.json": {Data: []byte(strings.ReplaceAll(entry, "@", srv.URL))}}, "data")
	if err != nil {
		t.Fatal(err)
	}
	st := store.Migrated(t)
	if _, err := st.Connections().Create(context.Background(), repository.ConnInput{
		Provider: "openai", AuthType: "apikey", Name: "k", Fields: map[string]string{"apiKey": `"KEY"`},
	}, nil); err != nil {
		t.Fatal(err)
	}
	sink := &usage.Sink{}
	p := gatewaymedia.New(gatewaymedia.Deps{
		Registry: reg, Adapters: mediamux.New(), Client: gatewaymedia.HTTP{Client: srv.Client()},
		Selector: account.NewSelector(account.SelectorOptions{Conns: st.Connections(), Settings: st.Settings()}),
		Reporter: account.NewReporter(st.Connections(), &tests.Log{}, nil),
		Usage:    sink, Now: time.Now,
	})
	return p, sink
}

const openAIAudio = `{"id":"openai","category":"apikey","display":{"name":"OpenAI"},
"media":{"serviceKinds":["tts","stt"],
  "ttsConfig":{"baseUrl":"@/v1/audio/speech","authType":"apikey","authHeader":"bearer","defaultModel":"gpt-4o-mini-tts","models":[{"id":"tts-1","name":"TTS-1"}]},
  "sttConfig":{"baseUrl":"@/v1/audio/transcriptions","authType":"apikey","authHeader":"bearer"}},
"models":[{"id":"tts-1","name":"TTS-1","kind":"tts"},{"id":"whisper-1","name":"Whisper","kind":"stt"}]}`

func TestSpeechAnswersAudioOrJSON(t *testing.T) {
	audio := strings.Repeat("a", 200)
	p, sink := audioRig(t, openAIAudio, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte(audio))
	})
	out := p.Speech(context.Background(), gatewaymedia.Input{Path: "/v1/audio/speech", Body: []byte(`{"model":"openai/tts-1/nova","input":"hello"}`)})
	if out.Status != 200 || string(out.Body) != audio || out.Header["Content-Type"] != "audio/mpeg" {
		t.Fatalf("%d %v", out.Status, out.Header)
	}
	if row := sink.All(); len(row) != 1 || row[0].Endpoint != "/v1/audio/speech" {
		t.Fatalf("%+v", row)
	}
	js := p.Speech(context.Background(), gatewaymedia.Input{Path: "/v1/audio/speech", ResponseFormat: "json", Body: []byte(`{"model":"openai/tts-1/nova","input":"hello"}`)})
	if js.Status != 200 || js.Header["Content-Type"] != "application/json" {
		t.Fatalf("%d %v", js.Status, js.Header)
	}
	got, err := base64.StdEncoding.DecodeString(gjson.GetBytes(js.Body, "audio").String())
	if err != nil || string(got) != audio || gjson.GetBytes(js.Body, "format").String() != "mp3" {
		t.Fatalf("%s %v", js.Body, err)
	}
}

func TestTranscriptionsTakesTheForm(t *testing.T) {
	p, _ := audioRig(t, openAIAudio, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"text":"hello there"}`))
	})
	form := &gatewaymedia.Form{Audio: []byte("audio"), Filename: "a.mp3", ContentType: "audio/mpeg",
		Fields: map[string]string{"model": "whisper-1"}}
	out := p.Transcriptions(context.Background(), gatewaymedia.Input{Path: "/v1/audio/transcriptions", Form: form})
	if out.Status != 200 || gjson.GetBytes(out.Body, "text").String() != "hello there" {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
}

func TestAnOversizedUploadIsRefused(t *testing.T) {
	p, _ := audioRig(t, openAIAudio, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"text":"x"}`))
	})
	form := &gatewaymedia.Form{Audio: make([]byte, gatewaymedia.MaxAudioBytes+1), Filename: "a.wav",
		Fields: map[string]string{"model": "whisper-1"}}
	if out := p.Transcriptions(context.Background(), gatewaymedia.Input{Form: form}); out.Status != http.StatusBadRequest {
		t.Fatalf("%d", out.Status)
	}
}

func TestVoicesComeFromTheProviderData(t *testing.T) {
	entry := `{"id":"openai","category":"apikey","display":{"name":"OpenAI"},
"media":{"serviceKinds":["tts"],"ttsConfig":{"baseUrl":"https://x.example/speech","authType":"apikey","authHeader":"bearer",
"voices":[{"id":"alloy","name":"Alloy"},{"id":"nova","name":"Nova","lang":"en-US"}]}},"models":[]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	reg, err := upstream.Load(fstest.MapFS{"data/openai.json": {Data: []byte(entry)}}, "data")
	if err != nil {
		t.Fatal(err)
	}
	st := store.Migrated(t)
	p := gatewaymedia.New(gatewaymedia.Deps{
		Registry: reg, Adapters: mediamux.New(), Client: gatewaymedia.HTTP{Client: srv.Client()},
		Selector: account.NewSelector(account.SelectorOptions{Conns: st.Connections(), Settings: st.Settings()}),
		Reporter: account.NewReporter(st.Connections(), &tests.Log{}, nil), Usage: &usage.Sink{}, Now: time.Now,
	})
	out := p.Voices(context.Background(), "openai", "")
	if out.Status != 200 || len(gjson.GetBytes(out.Body, "data").Array()) != 2 ||
		gjson.GetBytes(out.Body, "data.0.model").String() != "openai/alloy" {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	filtered := p.Voices(context.Background(), "openai", "en-US")
	if len(gjson.GetBytes(filtered.Body, "data").Array()) != 1 {
		t.Fatalf("%s", filtered.Body)
	}
	if out := p.Voices(context.Background(), "nope", ""); out.Status != http.StatusBadRequest {
		t.Fatalf("%d", out.Status)
	}
}
