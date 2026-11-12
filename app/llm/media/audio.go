package media

import (
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
)

// Upload is the one file a transcription request carries.
type Upload struct {
	Audio       []byte
	Filename    string
	ContentType string
	// Fields are the form fields beside the file.
	Fields map[string]string
}

// audioMIMEs maps a requested format to the type its bytes are served as.
var audioMIMEs = map[string]string{
	"mp3": "audio/mpeg", "mpeg": "audio/mpeg", "wav": "audio/wav", "ogg": "audio/ogg",
	"opus": "audio/opus", "aac": "audio/aac", "flac": "audio/flac", "pcm": "audio/pcm",
}

// AudioMIME names the type of a requested audio format.
func AudioMIME(format string) string {
	if m, ok := audioMIMEs[strings.ToLower(format)]; ok {
		return m
	}
	return "audio/mpeg"
}

// AudioFormat names the format of an upstream answer from its content type, or
// the fallback when the type is unfamiliar.
func AudioFormat(contentType, fallback string) string {
	ct := strings.ToLower(contentType)
	switch {
	case strings.Contains(ct, "wav"):
		return "wav"
	case strings.Contains(ct, "mpeg"), strings.Contains(ct, "mp3"):
		return "mp3"
	case strings.Contains(ct, "ogg"):
		return "ogg"
	case strings.Contains(ct, "opus"):
		return "opus"
	case strings.Contains(ct, "pcm"):
		return "pcm"
	}
	return fallback
}

// ParseModelVoice splits a TTS model name into its model and voice. A known
// model id wins first (longest first), then the last slash holds the voice.
func ParseModelVoice(name, defaultModel, defaultVoice string, known []string) (model, voice string) {
	if name == "" {
		return defaultModel, defaultVoice
	}
	longest := ""
	for _, id := range known {
		if id != "" && (id == name || strings.HasPrefix(name, id+"/")) && len(id) > len(longest) {
			longest = id
		}
	}
	if longest != "" {
		if name == longest {
			return longest, defaultVoice
		}
		return longest, strings.TrimPrefix(name, longest+"/")
	}
	if i := strings.LastIndexByte(name, '/'); i > 0 {
		return name[:i], name[i+1:]
	}
	if defaultModel == "" {
		defaultModel = name
	}
	if defaultVoice == "" {
		defaultVoice = name
	}
	return defaultModel, defaultVoice
}

// PassRaw is the answer of an adapter that returns text or audio bytes: a
// refusal is a provider error, anything else passes with its own type.
func PassRaw(status int, header http.Header, body []byte, contentType, refusal string) (Response, error) {
	if status < 200 || status > 299 {
		return Response{}, &ProviderError{Status: status, Text: refusal}
	}
	if len(body) == 0 {
		return Response{}, &ProviderError{Status: http.StatusBadGateway, Text: refusal}
	}
	h := map[string]string{}
	if ct := header.Get("Content-Type"); ct != "" {
		h["Content-Type"] = ct
	} else {
		h["Content-Type"] = contentType
	}
	return Response{Status: status, Header: h, Body: body}, nil
}

// AudioAnswer wraps raw audio in the client's shape: bare bytes with their
// type, or the JSON envelope a caller asked for.
func AudioAnswer(audio []byte, format string, asJSON bool) (Response, error) {
	if len(audio) < 100 {
		return Response{}, &ProviderError{Status: http.StatusBadGateway, Text: "the provider returned no audio"}
	}
	if asJSON {
		body := []byte(`{"audio":"` + base64.StdEncoding.EncodeToString(audio) + `","format":"` + format + `"}`)
		return Response{Status: http.StatusOK, Header: map[string]string{"Content-Type": "application/json"}, Body: body}, nil
	}
	return Response{Status: http.StatusOK, Header: map[string]string{
		"Content-Type":   AudioMIME(format),
		"Content-Length": strconv.Itoa(len(audio)),
	}, Body: audio}, nil
}
