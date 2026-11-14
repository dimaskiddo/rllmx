package controllers

import (
	"errors"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	contractshttp "github.com/goravel/framework/contracts/http"

	"rllmx/app/access/identity"
	gatewaymedia "rllmx/app/gateway/media"
	apphttp "rllmx/app/http"
	"rllmx/tests"
)

// withCaller stands in for the guard: it records who the request acts as, then
// runs the handler.
func withCaller(c identity.Caller, h contractshttp.HandlerFunc) contractshttp.HandlerFunc {
	return func(ctx contractshttp.Context) contractshttp.Response {
		apphttp.WithCaller(ctx, c)
		return h(ctx)
	}
}

func TestAKeyLimitedToModelsCannotSpendThem(t *testing.T) {
	chat := &ChatController{}
	media := &MediaController{}
	routes := map[string]contractshttp.HandlerFunc{
		"/v1/chat/completions":      chat.Serve,
		"/v1/messages/count_tokens": chat.CountTokens,
		"/v1/embeddings":            media.Embeddings,
		"/v1/images/generations":    media.Images,
		"/v1/audio/speech":          media.Speech,
		"/v1/audio/transcriptions":  media.Transcriptions,
		"/v1/audio/voices":          media.Voices,
		"/v1/videos/generations":    media.VideoGenerations,
		"/v1/videos/edits":          media.VideoEdits,
		"/v1/videos/extensions":     media.VideoExtensions,
		"/v1/search":                media.Search,
		"/v1/web/fetch":             media.WebFetch,
	}
	for path, handler := range routes {
		t.Run(path, func(t *testing.T) {
			restricted := identity.Caller{ID: "k1", Restricted: true, Allow: []string{"openai/gpt-4o"}}
			r := tests.NewRoute(t)
			r.SetGlobalMiddleware(nil)
			r.Post(path, withCaller(restricted, handler))
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(nethttp.MethodPost, path, strings.NewReader(`{"model":"m"}`))
			req.RemoteAddr = "203.0.113.9:9999"
			r.ServeHTTP(rec, req)
			if rec.Code != nethttp.StatusForbidden {
				t.Fatalf("restricted key: %d %s", rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "gpt-4o") {
				t.Fatal("the refusal named the key's own list")
			}
		})
	}
}

func TestAnOpenKeyReachesThePipelineGate(t *testing.T) {
	open := identity.Caller{ID: "k2"}
	unavailable := func() (*gatewaymedia.Pipeline, error) { return nil, errors.New("stores not open") }
	r := tests.NewRoute(t)
	r.SetGlobalMiddleware(nil)
	r.Post("/v1/embeddings", withCaller(open, (&MediaController{EmbeddingsPipe: unavailable}).Embeddings))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(nethttp.MethodPost, "/v1/embeddings", strings.NewReader(`{"model":"m"}`))
	req.RemoteAddr = "203.0.113.9:9999"
	r.ServeHTTP(rec, req)
	// No pipeline is wired, so an allowed caller is answered 503, not 401 or 403.
	if rec.Code != nethttp.StatusServiceUnavailable {
		t.Fatalf("open key: %d", rec.Code)
	}
}
