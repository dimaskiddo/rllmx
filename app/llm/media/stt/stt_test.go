package stt

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"mime"
	"mime/multipart"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
)

func req(fields map[string]string, audio []byte) media.Request {
	if fields == nil {
		fields = map[string]string{}
	}
	return media.Request{Kind: upstream.KindSTT, Provider: "p", Model: fields["model"], Credential: "KEY",
		Config: &upstream.MediaConfig{BaseURL: "https://api.example/v1"},
		Upload: &media.Upload{Audio: audio, Filename: "a.mp3", ContentType: "audio/mpeg", Fields: fields}}
}

func TestOpenAISendsTheForm(t *testing.T) {
	r := req(map[string]string{"model": "whisper-1", "language": "en", "temperature": "0"}, []byte("audio"))
	call, err := (OpenAI{}).Build(context.Background(), r, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, params, _ := mime.ParseMediaType(call.Header["Content-Type"])
	form, err := multipart.NewReader(strings.NewReader(string(call.Body)), params["boundary"]).ReadForm(1 << 20)
	if err != nil {
		t.Fatal(err)
	}
	if form.Value["model"][0] != "whisper-1" || form.Value["language"][0] != "en" || form.File["file"][0].Filename != "a.mp3" {
		t.Fatalf("%v %v", form.Value, form.File)
	}
}

func TestDeepgramSendsRawAudioAndItsQuery(t *testing.T) {
	r := req(map[string]string{"model": "nova-3"}, []byte("audio"))
	r.Config.BaseURL = "https://api.deepgram.com/v1/listen"
	call, err := (Deepgram{}).Build(context.Background(), r, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(call.URL, "model=nova-3") || !strings.Contains(call.URL, "detect_language=true") ||
		string(call.Body) != "audio" || call.Header["Authorization"] != "Token KEY" {
		t.Fatalf("%s %s %v", call.URL, call.Body, call.Header)
	}
}

func TestElevenLabsAddsTheRequestedFormat(t *testing.T) {
	r := req(map[string]string{"model": "scribe_v1", "response_format": "srt", "diarize": "true"}, []byte("audio"))
	r.Config.ResponseFormats = map[string]string{"subtitles": "srt", "segments": "seg_json"}
	call, err := (ElevenLabs{}).Build(context.Background(), r, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, params, _ := mime.ParseMediaType(call.Header["Content-Type"])
	form, _ := multipart.NewReader(strings.NewReader(string(call.Body)), params["boundary"]).ReadForm(1 << 20)
	if !strings.Contains(form.Value["additional_formats"][0], `"format":"srt"`) || form.Value["diarize"][0] != "true" {
		t.Fatalf("%v", form.Value)
	}
	body, _ := json.Marshal(map[string]any{"text": "full", "additional_formats": []map[string]string{{"format": "srt", "content": "1\n00:00"}}})
	out, err := (ElevenLabs{}).Parse(context.Background(), r, nil, 200, nil, body)
	if err != nil || string(out.Body) != "1\n00:00" || out.Header["Content-Type"] != "text/plain" {
		t.Fatalf("%s %v", out.Body, err)
	}
}

func wavBytes(rate int, channels int) []byte {
	const pcm = 2400
	b := make([]byte, 44+pcm)
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(36+pcm))
	copy(b[8:], "WAVE")
	copy(b[12:], "fmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], uint16(channels))
	binary.LittleEndian.PutUint32(b[24:], uint32(rate))
	binary.LittleEndian.PutUint32(b[28:], uint32(rate*channels*2))
	binary.LittleEndian.PutUint16(b[32:], uint16(channels*2))
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], pcm)
	return b
}

func TestCodexTakesOnlyTheWavItNeeds(t *testing.T) {
	if !wav24kMono(wavBytes(24000, 1)) {
		t.Fatal("a 24k mono wav was refused")
	}
	for name, b := range map[string][]byte{
		"a stereo wav": wavBytes(24000, 2),
		"a 48k wav":    wavBytes(48000, 1),
		"not a wav":    []byte("audio"),
		"a short file": wavBytes(24000, 1)[:20],
		"an 8-bit wav": func() []byte {
			b := wavBytes(24000, 1)
			binary.LittleEndian.PutUint16(b[34:], 8)
			return b
		}(),
	} {
		if wav24kMono(b) {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestGeminiLiveBuildsTheBidiAddress(t *testing.T) {
	r := req(map[string]string{"model": "gemini-2.5-flash-native-audio-preview"}, nil)
	r.Config.BaseURL = "https://generativelanguage.googleapis.com/v1beta/models"
	target, err := liveURL(r)
	if err != nil || !strings.HasPrefix(target, "wss://generativelanguage.googleapis.com/ws/api/v1beta/models/") ||
		!strings.Contains(target, ":bidiGenerateContent?key=KEY") {
		t.Fatalf("%s %v", target, err)
	}
}

func TestATranscriptIsRead(t *testing.T) {
	out, err := textAnswer(200, []byte(`{"text":"hello"}`))
	if err != nil || gjson.GetBytes(out.Body, "text").String() != "hello" {
		t.Fatalf("%s %v", out.Body, err)
	}
	if _, err := textAnswer(401, nil); err == nil {
		t.Fatal("a refusal was read as a transcript")
	}
}
