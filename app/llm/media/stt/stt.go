// Package stt serves the transcription APIs: one file, its fields and the
// provider's own answer shape.
package stt

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
)

func kind() string { return upstream.KindSTT }

func field(r media.Request, name string) string {
	if r.Upload == nil {
		return ""
	}
	return strings.TrimSpace(r.Upload.Fields[name])
}

// audioMIME is the upload's type: its own when it names one, else from the
// file name.
func audioMIME(r media.Request) string {
	if r.Upload == nil {
		return "application/octet-stream"
	}
	if strings.HasPrefix(r.Upload.ContentType, "audio/") {
		return r.Upload.ContentType
	}
	name := strings.ToLower(r.Upload.Filename)
	for ext, mime := range map[string]string{
		".mp3": "audio/mpeg", ".m4a": "audio/mp4", ".mp4": "audio/mp4", ".wav": "audio/wav",
		".ogg": "audio/ogg", ".flac": "audio/flac", ".webm": "audio/webm", ".aac": "audio/aac", ".opus": "audio/opus",
	} {
		if strings.HasSuffix(name, ext) {
			return mime
		}
	}
	return "application/octet-stream"
}

// multipartCall builds the form: the named fields when the client sent them, and
// the file.
func multipartCall(r media.Request, target string, header map[string]string, fields []struct{ form, upstream string }, extra [][2]string) (media.Call, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, f := range fields {
		if v := field(r, f.form); v != "" {
			_ = mw.WriteField(f.upstream, v)
		}
	}
	for _, pair := range extra {
		if pair[1] != "" {
			_ = mw.WriteField(pair[0], pair[1])
		}
	}
	name := "audio.wav"
	if r.Upload != nil && r.Upload.Filename != "" {
		name = r.Upload.Filename
	}
	part, err := mw.CreateFormFile("file", name)
	if err != nil {
		return media.Call{}, media.BadRequestf("the upload cannot be built")
	}
	if _, err := part.Write(r.Upload.Audio); err != nil {
		return media.Call{}, media.BadRequestf("the upload cannot be built")
	}
	if err := mw.Close(); err != nil {
		return media.Call{}, media.BadRequestf("the upload cannot be built")
	}
	h := map[string]string{}
	for k, v := range header {
		h[k] = v
	}
	h["Content-Type"] = mw.FormDataContentType()
	return media.Call{Method: http.MethodPost, URL: target, Header: h, Body: buf.Bytes()}, nil
}

// bearer is the account credential as a bearer header.
func bearer(r media.Request) map[string]string {
	h := map[string]string{}
	if r.Credential != "" {
		h["Authorization"] = "Bearer " + r.Credential
	}
	return h
}

func refusal(status int, text string) error {
	return &media.ProviderError{Status: status, Text: text}
}

// textAnswer is a JSON {text} answer.
func textAnswer(status int, body []byte) (media.Response, error) {
	if status < 200 || status > 299 {
		return media.Response{}, refusal(status, "the provider refused the transcription")
	}
	text := gjson.GetBytes(body, "text").String()
	if text == "" && !gjson.GetBytes(body, "text").Exists() {
		text = gjson.GetBytes(body, "transcript").String()
	}
	raw, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return media.Response{}, refusal(http.StatusBadGateway, "the provider answered no transcript")
	}
	return media.Response{Status: http.StatusOK, Header: map[string]string{"Content-Type": "application/json"}, Body: raw}, nil
}

// passthrough returns the provider's own answer, text or JSON, unchanged.
func passthrough(status int, header http.Header, body []byte) (media.Response, error) {
	if status < 200 || status > 299 {
		return media.Response{}, refusal(status, "the provider refused the transcription")
	}
	if len(body) == 0 {
		return media.Response{}, refusal(http.StatusBadGateway, "the provider answered no transcript")
	}
	ct := header.Get("Content-Type")
	if ct == "" {
		ct = "application/json"
	}
	return media.Response{Status: http.StatusOK, Header: map[string]string{"Content-Type": ct}, Body: body}, nil
}
