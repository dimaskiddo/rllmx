package controllers

import (
	"context"
	"io"
	nethttp "net/http"
	"strconv"
	"strings"

	contractshttp "github.com/goravel/framework/contracts/http"
	"github.com/goravel/framework/contracts/log"

	"rllmx/app/access/identity"
	"rllmx/app/access/quota"
	"rllmx/app/gateway/pipeline"
	apphttp "rllmx/app/http"
	"rllmx/app/http/middleware"
	"rllmx/app/llm/stream"
	"rllmx/app/llm/translate"
	"rllmx/app/platform/failure"
)

// ChatController serves the client chat endpoints through the pipeline.
type ChatController struct {
	// Pipe returns the pipeline, building it on first use. It fails while the
	// stores are not open.
	Pipe func() (*pipeline.Pipeline, error)
	Log  log.Log
	// Standing reads a key's budget standing so its answer can carry the
	// rate-limit headers a Claude client watches. Nil, or a read that fails,
	// leaves them off: they are a courtesy, never a reason to refuse.
	Standing func(ctx context.Context, keyID string) (quota.Standings, error)
}

// Serve answers a chat call in whatever format its path names: OpenAI, Claude,
// Responses, Gemini or Ollama.
func (c *ChatController) Serve(ctx contractshttp.Context) contractshttp.Response {
	r := ctx.Request().Origin()
	w := ctx.Response().Writer()
	format := failure.ForPath(r.URL.Path)

	caller, ok := apphttp.CallerFrom(ctx)
	if !ok {
		failure.Write(w, format, nethttp.StatusUnauthorized, "API key required")
		return nil
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		failure.Write(w, format, failure.ReadStatus(err), "")
		return nil
	}
	p, err := c.Pipe()
	if err != nil || p == nil {
		if err != nil && c.Log != nil {
			c.Log.Warningf("chat: pipeline unavailable: %v", err)
		}
		failure.Write(w, format, nethttp.StatusServiceUnavailable, "")
		return nil
	}

	c.rateLimitHeaders(ctx, caller)
	opts := stream.Options{WriteTimeout: stream.DefaultWriteTimeout, Log: c.Log}
	open := func() (stream.Client, error) { return stream.NewSSE(stream.WriterOf(ctx), opts) }
	if f, _ := translate.Detect(r.URL.Path); f == translate.FormatOllama {
		open = func() (stream.Client, error) { return stream.NewNDJSON(stream.WriterOf(ctx), opts) }
	}
	out := p.Handle(r.Context(), pipeline.Input{
		Path: r.URL.Path, Body: body, Caller: caller,
		RequestID: middleware.RequestIDFrom(ctx), UserAgent: r.UserAgent(), Header: pipeline.PickHeaders(r.Header),
		AcceptJSON: wantsJSON(r.Header.Get("Accept")),
		Cache:      cacheMode(r.Header.Get(pipeline.CacheHeader)),
	}, open)
	if out.Streamed || out.Status == 499 {
		return nil
	}
	writeAnswer(stream.WriterOf(ctx), out)
	return nil
}

// rateLimitHeaders puts the key's standing on the response before the pipeline
// runs, because a stream commits its headers with its first frame. A key with no
// budget gets none: there is nothing true to say.
func (c *ChatController) rateLimitHeaders(ctx contractshttp.Context, caller identity.Caller) {
	if c.Standing == nil || caller.ID == "" || caller.Anonymous {
		return
	}
	s, err := c.Standing(ctx.Request().Origin().Context(), caller.ID)
	if err != nil || (!s.Day.Limited && !s.Month.Limited && !s.Total.Limited) {
		return
	}
	tight := s.Day
	if s.Month.Percent > tight.Percent {
		tight = s.Month
	}
	for name, vals := range failure.RateLimitHeaders(tight.Percent >= 100, tight.ResetsAt) {
		ctx.Response().Header(name, vals[0])
	}
}

// writeAnswer sends a buffered answer through the writer behind the driver's
// body copy, which would otherwise keep a second copy of up to 64 MiB for the
// life of the request.
func writeAnswer(w nethttp.ResponseWriter, out pipeline.Output) {
	h := w.Header()
	for k, v := range out.Header {
		h[k] = v
	}
	w.WriteHeader(out.Status)
	_, _ = w.Write(out.Body)
}

// cacheMode reads the client's X-Rllm-Cache wish. Only "use" and "bypass" mean
// anything; every other value is no wish.
func cacheMode(v string) string {
	switch m := strings.ToLower(strings.TrimSpace(v)); m {
	case "use", "bypass":
		return m
	}
	return ""
}

// wantsJSON is true for an Accept header that names JSON and not an event stream.
func wantsJSON(accept string) bool {
	return strings.Contains(accept, "application/json") && !strings.Contains(accept, "text/event-stream")
}

// CountTokens answers POST /v1/messages/count_tokens with a local estimate. It
// makes no upstream call and leaves no usage row.
func (c *ChatController) CountTokens(ctx contractshttp.Context) contractshttp.Response {
	r := ctx.Request().Origin()
	w := ctx.Response().Writer()
	format := failure.ForPath(r.URL.Path)
	caller, ok := apphttp.CallerFrom(ctx)
	if !ok {
		failure.Write(w, format, nethttp.StatusUnauthorized, "API key required")
		return nil
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		failure.Write(w, format, failure.ReadStatus(err), "")
		return nil
	}
	codec, ok := translate.Lookup(translate.FormatClaude)
	if !ok {
		failure.Write(w, format, nethttp.StatusInternalServerError, "")
		return nil
	}
	req, err := codec.ReadRequest(body, translate.NewCtx(translate.FormatClaude, translate.FormatClaude))
	if err != nil {
		failure.Write(w, format, nethttp.StatusBadRequest, "Invalid request body")
		return nil
	}
	// A key that may not call the model hears so here too: this route spends
	// nothing, but it names a model, so it must not confirm one the key may not
	// use.
	if caller.Restricted {
		p, err := c.Pipe()
		if err != nil || p == nil {
			failure.Write(w, format, nethttp.StatusServiceUnavailable, "")
			return nil
		}
		if !p.Allows(r.Context(), caller, req.Model) {
			failure.Write(w, format, nethttp.StatusForbidden, failure.NotAllowed(req.Model))
			return nil
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(nethttp.StatusOK)
	_, _ = w.Write([]byte(`{"input_tokens":` + strconv.FormatInt(translate.CountTokens(req), 10) + `}`))
	return nil
}
