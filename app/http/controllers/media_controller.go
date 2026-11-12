package controllers

import (
	"io"
	nethttp "net/http"

	contractshttp "github.com/goravel/framework/contracts/http"
	"github.com/goravel/framework/contracts/log"

	gatewaymedia "rllmx/app/gateway/media"
	apphttp "rllmx/app/http"
	"rllmx/app/http/middleware"
	"rllmx/app/platform/failure"
)

// MediaController serves the non-chat endpoints. Each source builds its
// pipeline on first use, because the stores open after the routes are declared.
type MediaController struct {
	EmbeddingsPipe func() (*gatewaymedia.Pipeline, error)
	ImagesPipe     func() (*gatewaymedia.Pipeline, error)
	Log            log.Log
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
		Binary: r.URL.Query().Get("response_format") == "binary",
	})
	for k, v := range out.Header {
		w.Header().Set(k, v)
	}
	w.WriteHeader(out.Status)
	_, _ = w.Write(out.Body)
	return nil
}
