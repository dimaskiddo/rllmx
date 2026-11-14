package stt

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
)

// Codex transcribes through ChatGPT's endpoint. The reference converts audio
// with ffmpeg; here the upload must already be the PCM WAV the endpoint takes.
type Codex struct {
	// slots bounds the concurrent transcriptions this account area runs.
	slots chan struct{}
}

// NewCodex builds the adapter with its two-slot bound.
func NewCodex() *Codex { return &Codex{slots: make(chan struct{}, 2)} }

// Kind is the service it serves.
func (Codex) Kind() string { return kind() }

// Direct runs the call: no conversion, only a WAV the endpoint accepts.
func (c *Codex) Direct(ctx context.Context, r media.Request, cl media.Client) (media.Response, error) {
	select {
	case c.slots <- struct{}{}:
		defer func() { <-c.slots }()
	default:
		return media.Response{}, refusal(http.StatusServiceUnavailable, "transcription capacity reached")
	}
	if !wav24kMono(r.Upload.Audio) {
		return media.Response{}, media.BadRequestf("the audio must be 16-bit PCM mono 24 kHz WAV")
	}
	model := field(r, "model")
	if model != "" && model != "transcribe" {
		return media.Response{}, media.BadRequestf("use transcribe for this provider")
	}
	for _, name := range []string{"language", "prompt", "temperature"} {
		if field(r, name) != "" {
			return media.Response{}, media.BadRequestf("this provider takes no " + name)
		}
	}
	header := map[string]string{
		"Authorization": "Bearer " + r.Credential,
		"User-Agent":    "codex_cli_rs/0.159.0",
	}
	if account := strings.TrimSpace(r.Account["chatgptAccountId"]); account != "" {
		header["ChatGPT-Account-Id"] = account
	}
	target := "https://chatgpt.com/backend-api/transcribe"
	if r.Config != nil && r.Config.BaseURL != "" {
		target = r.Config.BaseURL
	}
	call, err := multipartCall(r, target, header, nil, nil)
	if err != nil {
		return media.Response{}, err
	}
	status, _, body, err := cl.Do(ctx, call.Method, call.URL, call.Header, call.Body)
	if err != nil {
		return media.Response{}, err
	}
	if status < 200 || status > 299 {
		return media.Response{}, refusal(status, "the provider refused the transcription")
	}
	if field(r, "response_format") == "text" {
		return media.Response{Status: http.StatusOK, Header: map[string]string{"Content-Type": "text/plain", "Cache-Control": "no-store"}, Body: []byte(gjson.GetBytes(body, "text").String())}, nil
	}
	raw, _ := json.Marshal(map[string]string{"text": gjson.GetBytes(body, "text").String()})
	return media.Response{Status: http.StatusOK, Header: map[string]string{"Content-Type": "application/json", "Cache-Control": "no-store"}, Body: raw}, nil
}

// Build is unused: Direct runs the whole exchange.
func (c *Codex) Build(context.Context, media.Request, media.Client) (media.Call, error) {
	return media.Call{}, media.BadRequestf("this adapter runs its own exchange")
}

// Parse is unused.
func (c *Codex) Parse(context.Context, media.Request, media.Client, int, http.Header, []byte) (media.Response, error) {
	return media.Response{}, media.BadRequestf("this adapter runs its own exchange")
}

// wav24kMono reports a RIFF/WAVE file that is 16-bit PCM, one channel, 24 kHz,
// and at most 120 seconds.
func wav24kMono(b []byte) bool {
	if len(b) < 44 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return false
	}
	var channels, rate, bits int
	for off := 12; off+8 <= len(b); {
		id := string(b[off : off+4])
		size := int(binary.LittleEndian.Uint32(b[off+4 : off+8]))
		body := off + 8
		if size < 0 || body+size > len(b) {
			return false
		}
		switch id {
		case "fmt ":
			if size < 16 {
				return false
			}
			format := binary.LittleEndian.Uint16(b[body : body+2])
			channels = int(binary.LittleEndian.Uint16(b[body+2 : body+4]))
			rate = int(binary.LittleEndian.Uint32(b[body+4 : body+8]))
			bits = int(binary.LittleEndian.Uint16(b[body+14 : body+16]))
			if format != 1 {
				return false
			}
		case "data":
			if channels != 1 || rate != 24000 || bits != 16 {
				return false
			}
			return size <= 120*24000*2
		}
		off = body + size
		off += off % 2
	}
	return false
}
