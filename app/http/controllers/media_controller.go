package controllers

import (
	"io"
	nethttp "net/http"

	contractshttp "github.com/goravel/framework/contracts/http"
	"github.com/goravel/framework/contracts/log"

	gatewaymedia "rllmx/app/gateway/media"
	apphttp "rllmx/app/http"
	"rllmx/app/http/middleware"
	"rllmx/app/llm/media"
	"rllmx/app/platform/failure"
)

// MediaController serves the non-chat endpoints. Each source builds its
// pipeline on first use, because the stores open after the routes are declared.
type MediaController struct {
	EmbeddingsPipe func() (*gatewaymedia.Pipeline, error)
	ImagesPipe     func() (*gatewaymedia.Pipeline, error)
	SpeechPipe     func() (*gatewaymedia.Pipeline, error)
	TranscribePipe func() (*gatewaymedia.Pipeline, error)
	VoicesPipe     func() (*gatewaymedia.Pipeline, error)
	VideosPipe     func() (*gatewaymedia.Pipeline, error)
	SearchPipe     func() (*gatewaymedia.Pipeline, error)
	FetchPipe      func() (*gatewaymedia.Pipeline, error)
	Log            log.Log
}

// Search answers POST /v1/search: a query, and the provider that searches for it.
func (m *MediaController) Search(ctx contractshttp.Context) contractshttp.Response {
	r := ctx.Request().Origin()
	return m.serve(ctx, m.SearchPipe, func(p *gatewaymedia.Pipeline, in gatewaymedia.Input) gatewaymedia.Output {
		return p.Search(r.Context(), in)
	})
}

// WebFetch answers POST /v1/web/fetch: a URL to read.
func (m *MediaController) WebFetch(ctx contractshttp.Context) contractshttp.Response {
	r := ctx.Request().Origin()
	return m.serve(ctx, m.FetchPipe, func(p *gatewaymedia.Pipeline, in gatewaymedia.Input) gatewaymedia.Output {
		return p.Fetch(r.Context(), in)
	})
}

// VideoGenerations answers POST /v1/videos/generations.
func (m *MediaController) VideoGenerations(ctx contractshttp.Context) contractshttp.Response {
	return m.videos(ctx, media.VideoGenerations)
}

// VideoEdits answers POST /v1/videos/edits.
func (m *MediaController) VideoEdits(ctx contractshttp.Context) contractshttp.Response {
	return m.videos(ctx, media.VideoEdits)
}

// VideoExtensions answers POST /v1/videos/extensions.
func (m *MediaController) VideoExtensions(ctx contractshttp.Context) contractshttp.Response {
	return m.videos(ctx, media.VideoExtensions)
}

func (m *MediaController) videos(ctx contractshttp.Context, action string) contractshttp.Response {
	r := ctx.Request().Origin()
	return m.serve(ctx, m.VideosPipe, func(p *gatewaymedia.Pipeline, in gatewaymedia.Input) gatewaymedia.Output {
		return p.Videos(r.Context(), in, action)
	})
}

// VideoJob answers GET /v1/videos/{id}: a job's status.
func (m *MediaController) VideoJob(ctx contractshttp.Context) contractshttp.Response {
	r := ctx.Request().Origin()
	id := ctx.Request().Route("id")
	return m.serve(ctx, m.VideosPipe, func(p *gatewaymedia.Pipeline, in gatewaymedia.Input) gatewaymedia.Output {
		return p.VideoJob(r.Context(), in, id)
	})
}

// Speech answers POST /v1/audio/speech.
func (m *MediaController) Speech(ctx contractshttp.Context) contractshttp.Response {
	r := ctx.Request().Origin()
	return m.serve(ctx, m.SpeechPipe, func(p *gatewaymedia.Pipeline, in gatewaymedia.Input) gatewaymedia.Output {
		return p.Speech(r.Context(), in)
	})
}

// Transcriptions answers POST /v1/audio/transcriptions.
func (m *MediaController) Transcriptions(ctx contractshttp.Context) contractshttp.Response {
	r := ctx.Request().Origin()
	w := ctx.Response().Writer()
	caller, ok := apphttp.CallerFrom(ctx)
	if !ok {
		failure.Write(w, failure.OpenAI, nethttp.StatusUnauthorized, "API key required")
		return nil
	}
	form, status, msg := parseUpload(r)
	if form == nil {
		failure.Write(w, failure.OpenAI, status, msg)
		return nil
	}
	p, err := m.TranscribePipe()
	if err != nil || p == nil {
		if err != nil && m.Log != nil {
			m.Log.Warningf("media: pipeline unavailable: %v", err)
		}
		failure.Write(w, failure.OpenAI, nethttp.StatusServiceUnavailable, "")
		return nil
	}
	out := p.Transcriptions(r.Context(), gatewaymedia.Input{
		Path: r.URL.Path, Form: form, RequestID: middleware.RequestIDFrom(ctx),
		APIKeyID: caller.ID, KeyName: caller.Name,
	})
	for k, v := range out.Header {
		w.Header().Set(k, v)
	}
	w.WriteHeader(out.Status)
	_, _ = w.Write(out.Body)
	return nil
}

// Voices answers GET /v1/audio/voices.
func (m *MediaController) Voices(ctx contractshttp.Context) contractshttp.Response {
	r := ctx.Request().Origin()
	w := ctx.Response().Writer()
	if _, ok := apphttp.CallerFrom(ctx); !ok {
		failure.Write(w, failure.OpenAI, nethttp.StatusUnauthorized, "API key required")
		return nil
	}
	p, err := m.VoicesPipe()
	if err != nil || p == nil {
		failure.Write(w, failure.OpenAI, nethttp.StatusServiceUnavailable, "")
		return nil
	}
	q := r.URL.Query()
	out := p.Voices(r.Context(), q.Get("provider"), q.Get("lang"))
	for k, v := range out.Header {
		w.Header().Set(k, v)
	}
	w.WriteHeader(out.Status)
	_, _ = w.Write(out.Body)
	return nil
}

// parseUpload reads one multipart file and its fields, bounded in memory.
func parseUpload(r *nethttp.Request) (*gatewaymedia.Form, int, string) {
	reader, err := r.MultipartReader()
	if err != nil {
		return nil, nethttp.StatusBadRequest, "The request is not multipart form data"
	}
	form := &gatewaymedia.Form{Fields: map[string]string{}}
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nethttp.StatusBadRequest, "The upload could not be read"
		}
		if part.FileName() != "" {
			if form.Audio != nil {
				return nil, nethttp.StatusBadRequest, "The request carries more than one file"
			}
			audio, err := io.ReadAll(io.LimitReader(part, gatewaymedia.MaxAudioBytes+1))
			if err != nil || len(audio) == 0 {
				return nil, nethttp.StatusBadRequest, "The upload could not be read"
			}
			if len(audio) > gatewaymedia.MaxAudioBytes {
				return nil, nethttp.StatusRequestEntityTooLarge, "The audio is too large"
			}
			form.Audio, form.Filename, form.ContentType = audio, part.FileName(), part.Header.Get("Content-Type")
			continue
		}
		name := part.FormName()
		value, err := io.ReadAll(io.LimitReader(part, 1<<20+1))
		if err != nil || len(value) > 1<<20 {
			return nil, nethttp.StatusBadRequest, "A form field is too long"
		}
		if len(form.Fields) >= 32 {
			return nil, nethttp.StatusBadRequest, "The request carries too many fields"
		}
		form.Fields[name] = string(value)
	}
	if form.Audio == nil {
		return nil, nethttp.StatusBadRequest, "The request names no file"
	}
	return form, 0, ""
}

// Embeddings answers POST /v1/embeddings.
func (m *MediaController) Embeddings(ctx contractshttp.Context) contractshttp.Response {
	r := ctx.Request().Origin()
	return m.serve(ctx, m.EmbeddingsPipe, func(p *gatewaymedia.Pipeline, in gatewaymedia.Input) gatewaymedia.Output {
		return p.Embeddings(r.Context(), in)
	})
}

// Images answers POST /v1/images/generations.
func (m *MediaController) Images(ctx contractshttp.Context) contractshttp.Response {
	r := ctx.Request().Origin()
	return m.serve(ctx, m.ImagesPipe, func(p *gatewaymedia.Pipeline, in gatewaymedia.Input) gatewaymedia.Output {
		return p.Images(r.Context(), in)
	})
}

func (m *MediaController) serve(ctx contractshttp.Context, source func() (*gatewaymedia.Pipeline, error), call func(*gatewaymedia.Pipeline, gatewaymedia.Input) gatewaymedia.Output) contractshttp.Response {
	r := ctx.Request().Origin()
	w := ctx.Response().Writer()
	caller, ok := apphttp.CallerFrom(ctx)
	if !ok {
		failure.Write(w, failure.OpenAI, nethttp.StatusUnauthorized, "API key required")
		return nil
	}
	p, err := source()
	if err != nil || p == nil {
		if err != nil && m.Log != nil {
			m.Log.Warningf("media: pipeline unavailable: %v", err)
		}
		failure.Write(w, failure.OpenAI, nethttp.StatusServiceUnavailable, "")
		return nil
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		failure.Write(w, failure.OpenAI, failure.ReadStatus(err), "The request could not be read")
		return nil
	}
	out := call(p, gatewaymedia.Input{
		Path: r.URL.Path, Body: body, RequestID: middleware.RequestIDFrom(ctx),
		APIKeyID: caller.ID, KeyName: caller.Name,
		Binary:         r.URL.Query().Get("response_format") == "binary",
		ResponseFormat: r.URL.Query().Get("response_format"),
		ContentType:    r.Header.Get("Content-Type"),
		IdempotencyKey: r.Header.Get("Idempotency-Key"),
		ConnectionID:   r.Header.Get("x-connection-id"),
		Provider:       r.URL.Query().Get("provider"),
	})
	for k, v := range out.Header {
		w.Header().Set(k, v)
	}
	w.WriteHeader(out.Status)
	_, _ = w.Write(out.Body)
	return nil
}

// TTSVoices answers the dashboard's voice list for a query provider.
func (m *MediaController) TTSVoices(ctx contractshttp.Context) contractshttp.Response {
	return m.dashboardVoices(ctx, ctx.Request().Origin().URL.Query().Get("provider"))
}

// ProviderVoices answers the dashboard's voice list for the path provider.
func (m *MediaController) ProviderVoices(ctx contractshttp.Context) contractshttp.Response {
	return m.dashboardVoices(ctx, ctx.Request().Origin().PathValue("provider"))
}

func (m *MediaController) dashboardVoices(ctx contractshttp.Context, provider string) contractshttp.Response {
	r := ctx.Request().Origin()
	w := ctx.Response().Writer()
	p, err := m.VoicesPipe()
	if err != nil || p == nil {
		failure.Write(w, failure.OpenAI, nethttp.StatusServiceUnavailable, "")
		return nil
	}
	if provider == "" {
		provider = r.URL.Query().Get("provider")
	}
	out := p.Voices(r.Context(), provider, r.URL.Query().Get("lang"))
	for k, v := range out.Header {
		w.Header().Set(k, v)
	}
	w.WriteHeader(out.Status)
	_, _ = w.Write(out.Body)
	return nil
}
