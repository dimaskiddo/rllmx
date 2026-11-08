package controllers

import (
	"io"
	nethttp "net/http"
	"strconv"
	"strings"

	contractshttp "github.com/goravel/framework/contracts/http"
	"github.com/goravel/framework/contracts/log"

	"rllmx/app/failure"
	apphttp "rllmx/app/http"
	"rllmx/app/http/middleware"
	"rllmx/app/pipeline"
	"rllmx/app/stream"
	"rllmx/app/translate"
)

// ChatController serves the client chat endpoints through the pipeline.
type ChatController struct {
	// Pipe returns the pipeline, building it on first use. It fails while the
	// stores are not open.
	Pipe func() (*pipeline.Pipeline, error)
	Log  log.Log
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

	opts := stream.Options{WriteTimeout: stream.DefaultWriteTimeout, Log: c.Log}
	open := func() (stream.Client, error) { return stream.NewSSE(stream.WriterOf(ctx), opts) }
	if f, _ := translate.Detect(r.URL.Path); f == translate.FormatOllama {
		open = func() (stream.Client, error) { return stream.NewNDJSON(stream.WriterOf(ctx), opts) }
	}
	out := p.Handle(r.Context(), pipeline.Input{
		Path: r.URL.Path, Body: body, Caller: caller,
		RequestID: middleware.RequestIdFrom(ctx), UserAgent: r.UserAgent(), Header: pipeline.PickHeaders(r.Header),
		AcceptJSON: wantsJSON(r.Header.Get("Accept")),
		Cache:      cacheMode(r.Header.Get(pipeline.CacheHeader)),
	}, open)
	if out.Streamed || out.Status == 499 {
		return nil
	}
	h := w.Header()
	for k, v := range out.Header {
		h[k] = v
	}
	w.WriteHeader(out.Status)
	_, _ = w.Write(out.Body)
	return nil
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
	if _, ok := apphttp.CallerFrom(ctx); !ok {
		failure.Write(w, format, nethttp.StatusUnauthorized, "API key required")
		return nil
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		failure.Write(w, format, failure.ReadStatus(err), "")
		return nil
	}
	req, err := (translate.ClaudeCodec{}).ReadRequest(body, translate.NewCtx(translate.FormatClaude, translate.FormatClaude))
	if err != nil {
		failure.Write(w, format, nethttp.StatusBadRequest, "Invalid request body")
		return nil
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(nethttp.StatusOK)
	_, _ = w.Write([]byte(`{"input_tokens":` + strconv.FormatInt(translate.CountTokens(req), 10) + `}`))
	return nil
}
