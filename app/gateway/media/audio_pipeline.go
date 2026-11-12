package media

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"rllmx/app/store/analytics"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
)

// Bounds on what one audio request may carry.
const (
	MaxSpeechInput = 32 << 10
	MaxAudioBytes  = 25 << 20
)

// speechFormats are the answer formats speech may ask for.
var speechFormats = map[string]bool{
	"mp3": true, "wav": true, "opus": true, "aac": true, "flac": true, "pcm": true, "json": true,
}

// transcriptFormats are the answer formats a transcription may ask for.
var transcriptFormats = map[string]bool{
	"json": true, "text": true, "srt": true, "vtt": true, "verbose_json": true,
}

// transcriptFields are the multipart fields a transcription may carry.
var transcriptFields = map[string]bool{
	"model": true, "response_format": true, "language": true, "prompt": true, "temperature": true,
	"timestamps_granularity": true, "tag_audio_events": true, "diarize": true, "num_speakers": true,
	"system_instruction": true, "setup_timeout_ms": true, "turn_timeout_ms": true,
}

// Speech serves POST /v1/audio/speech.
func (p *Pipeline) Speech(ctx context.Context, in Input) Output {
	req, bad, ok := parseJSON(in.Body)
	if !ok {
		return bad
	}
	format := strings.ToLower(strings.TrimSpace(firstNonEmpty(in.ResponseFormat, gjson.GetBytes(req.body, "response_format").String())))
	if format == "" {
		format = "mp3"
	}
	asJSON := format == "json"
	parsedFormat := format
	if asJSON {
		parsedFormat = "mp3"
	}
	out := p.run(ctx, upstream.KindTTS, in, req)
	if out.Status < 200 || out.Status > 299 || !asJSON {
		return out
	}
	actual := media.AudioFormat(out.Header["Content-Type"], parsedFormat)
	body, _ := json.Marshal(map[string]string{
		"audio": base64.StdEncoding.EncodeToString(out.Body), "format": actual,
	})
	return Output{Status: http.StatusOK, Header: map[string]string{"Content-Type": "application/json"}, Body: body}
}

// Transcriptions serves POST /v1/audio/transcriptions.
func (p *Pipeline) Transcriptions(ctx context.Context, in Input) Output {
	if in.Form == nil || len(in.Form.Audio) == 0 {
		return failureOutput(http.StatusBadRequest, "The request names no file")
	}
	model := strings.TrimSpace(in.Form.Fields["model"])
	if model == "" {
		return failureOutput(http.StatusBadRequest, "The request names no model")
	}
	req := parsed{
		model: model,
		upload: &media.Upload{
			Audio: in.Form.Audio, Filename: in.Form.Filename,
			ContentType: in.Form.ContentType, Fields: in.Form.Fields,
		},
	}
	return p.run(ctx, upstream.KindSTT, in, req)
}

// target resolves a kind's provider and entry. Speech accepts a provider-prefixed
// name that carries a voice, which no registry model id has to match.
func (p *Pipeline) target(ctx context.Context, kind, name string) (upstream.MediaRef, upstream.Entry, bool) {
	if kind == upstream.KindTTS {
		if provider, rest, ok := strings.Cut(name, "/"); ok && rest != "" {
			if id, resolved := p.d.Registry.Resolve(provider); resolved {
				if entry, found := p.d.Registry.ByID(id); found && entry.Media != nil && entry.Media.Config(upstream.KindTTS) != nil {
					return upstream.MediaRef{Provider: id, Kind: kind, Model: rest}, entry, true
				}
			}
		}
	}
	ref, ok := p.d.Registry.Media(name, kind)
	if !ok {
		if kind != upstream.KindEmbedding {
			return upstream.MediaRef{}, upstream.Entry{}, false
		}
		var entry upstream.Entry
		if !p.nodeRef(ctx, name, &ref, &entry) {
			return upstream.MediaRef{}, upstream.Entry{}, false
		}
		return ref, entry, true
	}
	entry, _ := p.d.Registry.ByID(ref.Provider)
	return ref, entry, true
}

// modelTransport is the per-model marker an STT task names (gemini-live).
func (p *Pipeline) modelTransport(ref upstream.MediaRef) string {
	entry, ok := p.d.Registry.ByID(ref.Provider)
	if !ok {
		return ""
	}
	for _, m := range entry.Models {
		if m.ID == ref.Model {
			return m.Transport
		}
	}
	return ""
}

// checkBody refuses what an audio endpoint cannot serve before any call.
func (p *Pipeline) checkBody(kind string, req parsed) error {
	switch kind {
	case upstream.KindTTS:
		input := gjson.GetBytes(req.body, "input").String()
		if strings.TrimSpace(input) == "" {
			return errors.New("the request names no input")
		}
		if len(input) > MaxSpeechInput {
			return errors.New("the input is too long")
		}
		if f := gjson.GetBytes(req.body, "response_format").String(); f != "" && !speechFormats[strings.ToLower(f)] {
			return errors.New("the answer format is not one this router serves")
		}
	case upstream.KindSTT:
		if req.upload == nil || len(req.upload.Audio) == 0 {
			return errors.New("the request names no file")
		}
		if len(req.upload.Audio) > MaxAudioBytes {
			return errors.New("the audio is too large")
		}
		if len(req.upload.Fields) > 32 {
			return errors.New("the request carries too many fields")
		}
		for k, v := range req.upload.Fields {
			if !transcriptFields[k] {
				return errors.New("the request carries a field this router does not serve")
			}
			if len(v) > 1<<20 {
				return errors.New("a field is too long")
			}
		}
		if f := req.upload.Fields["response_format"]; f != "" && !transcriptFormats[strings.ToLower(f)] {
			return errors.New("the answer format is not one this router serves")
		}
	default:
		return checkJSONBody(kind, req.body)
	}
	return nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// Voices serves GET /v1/audio/voices: the provider's voices from its data, or
// from its own API when it has a lister.
func (p *Pipeline) Voices(ctx context.Context, provider, lang string) Output {
	id, ok := p.d.Registry.Resolve(provider)
	if !ok {
		return failureOutput(http.StatusBadRequest, "Unknown provider")
	}
	entry, _ := p.d.Registry.ByID(id)
	cfg := entry.Media.Config(upstream.KindTTS)
	if cfg == nil {
		return failureOutput(http.StatusBadRequest, "That provider has no voices")
	}
	var list []upstream.Voice
	list = append(list, cfg.Voices...)
	for _, m := range cfg.Models {
		list = append(list, m.Voices...)
	}
	for _, m := range entry.Models {
		list = append(list, m.Voices...)
	}
	if p.d.Voices != nil {
		req := media.Request{Kind: upstream.KindTTS, Provider: id, Entry: entry, Config: cfg, Network: p.d.Network}
		if conn, out, ok := p.selectAccount(ctx, upstream.KindTTS, entry, upstream.MediaRef{Provider: id, Kind: upstream.KindTTS}, provider, map[string]bool{}, &analytics.Record{}, nil); ok {
			req.Credential = credential(conn, cfg)
			req.Account = accountData(conn)
		} else if len(list) == 0 {
			return out
		}
		callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		live, err := p.d.Voices(callCtx, id, req, p.d.Client)
		cancel()
		if err == nil && len(live) > 0 {
			list = live
		} else if len(list) == 0 {
			return failureOutput(http.StatusBadGateway, "The voice list is unavailable")
		}
	}
	if len(list) == 0 {
		return failureOutput(http.StatusBadRequest, "That provider has no voices")
	}
	alias := entry.Alias
	if alias == "" {
		alias = entry.ID
	}
	data := make([]map[string]string, 0, len(list))
	for _, v := range list {
		if v.ID == "" || (lang != "" && !strings.HasPrefix(strings.ToLower(v.Lang), strings.ToLower(lang))) {
			continue
		}
		data = append(data, map[string]string{
			"id": v.ID, "name": v.Name, "lang": v.Lang, "gender": v.Gender, "model": alias + "/" + v.ID,
		})
	}
	body, _ := json.Marshal(map[string]any{"object": "list", "data": data})
	return Output{Status: http.StatusOK, Header: map[string]string{"Content-Type": "application/json"}, Body: body}
}
