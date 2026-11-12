package stt

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
)

// ElevenLabs speaks its speech-to-text route and its response formats.
type ElevenLabs struct{}

// Kind is the service it serves.
func (ElevenLabs) Kind() string { return kind() }

// Build makes the call.
func (ElevenLabs) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	fields := []struct{ form, upstream string }{
		{"model", "model_id"}, {"language", "language_code"}, {"timestamps_granularity", "timestamps_granularity"},
	}
	var extra [][2]string
	if v := field(r, "tag_audio_events"); v == "true" {
		extra = append(extra, [2]string{"tag_audio_events", "true"})
	}
	if v := field(r, "diarize"); v == "true" {
		extra = append(extra, [2]string{"diarize", "true"})
	} else if v := field(r, "num_speakers"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 32 {
			extra = append(extra, [2]string{"num_speakers", v})
		}
	}
	if mapped := responseFormatMap(r); mapped != "" {
		encoded, _ := json.Marshal([]map[string]string{{"format": mapped}})
		extra = append(extra, [2]string{"additional_formats", string(encoded)})
	}
	h := map[string]string{"Accept": "application/json"}
	if r.Credential != "" {
		h["xi-api-key"] = r.Credential
	}
	return multipartCall(r, media.BaseURL(r), h, fields, extra)
}

// Parse serves the format the caller asked for.
func (ElevenLabs) Parse(_ context.Context, r media.Request, _ media.Client, status int, header http.Header, body []byte) (media.Response, error) {
	if status < 200 || status > 299 {
		return media.Response{}, refusal(status, "the provider refused the transcription")
	}
	mapped := responseFormatMap(r)
	want := field(r, "response_format")
	switch {
	case (want == "srt" || want == "vtt" || want == "verbose_json") && mapped != "":
		content := gjson.GetBytes(body, `additional_formats.#(format=="`+mapped+`").content`).String()
		if content == "" {
			content = gjson.GetBytes(body, "text").String()
		}
		if want == "verbose_json" {
			raw, _ := json.Marshal(map[string]any{
				"text": gjson.GetBytes(body, "text").String(), "language": gjson.GetBytes(body, "language_code").String(),
				"segments": json.RawMessage(orNull(content)),
			})
			return media.Response{Status: http.StatusOK, Header: map[string]string{"Content-Type": "application/json"}, Body: raw}, nil
		}
		return media.Response{Status: http.StatusOK, Header: map[string]string{"Content-Type": "text/plain"}, Body: []byte(content)}, nil
	case want == "text":
		return media.Response{Status: http.StatusOK, Header: map[string]string{"Content-Type": "text/plain"}, Body: []byte(gjson.GetBytes(body, "text").String())}, nil
	}
	return textAnswer(http.StatusOK, body)
}

func responseFormatMap(r media.Request) string {
	if r.Config == nil {
		return ""
	}
	switch field(r, "response_format") {
	case "verbose_json":
		return r.Config.ResponseFormats["segments"]
	case "srt":
		return r.Config.ResponseFormats["subtitles"]
	case "vtt":
		return r.Config.ResponseFormats["captions"]
	}
	return ""
}

func orNull(raw string) string {
	if raw == "" {
		return "null"
	}
	return raw
}
