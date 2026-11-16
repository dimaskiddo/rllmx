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

func TestNoCallerIsAnsweredWithAPIKeyRequired(t *testing.T) {
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
			r := tests.NewRoute(t)
			r.SetGlobalMiddleware(nil)
			r.Post(path, handler)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(nethttp.MethodPost, path, strings.NewReader(`{"model":"m"}`))
			req.RemoteAddr = "203.0.113.9:9999"
			r.ServeHTTP(rec, req)
			if rec.Code != nethttp.StatusUnauthorized {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestARestrictedKeyReachesThePipelineRatherThanBeingRefusedWholesale(t *testing.T) {
	// The list is enforced by the pipeline now, so the controller lets a
	// restricted key through; with no pipeline wired the answer is 503, not 403.
	restricted := identity.Caller{ID: "k1", Restricted: true, Allow: []string{"openai/gpt-4o"}}
	r := tests.NewRoute(t)
	r.SetGlobalMiddleware(nil)
	down := func() (*gatewaymedia.Pipeline, error) { return nil, errors.New("stores not open") }
	r.Post("/v1/embeddings", withCaller(restricted, (&MediaController{EmbeddingsPipe: down}).Embeddings))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(nethttp.MethodPost, "/v1/embeddings", strings.NewReader(`{"model":"m"}`))
	req.RemoteAddr = "203.0.113.9:9999"
	r.ServeHTTP(rec, req)
	if rec.Code != nethttp.StatusServiceUnavailable {
		t.Fatalf("a restricted key was refused by the controller: %d %s", rec.Code, rec.Body.String())
	}
}

func TestCountTokensIsCheckedAgainstTheList(t *testing.T) {
	body := `{"model":"` + chatModel + `","max_tokens":50,"messages":[{"role":"user","content":"hi"}]}`
	cases := []struct {
		name string
		key  identity.Caller
		want int
	}{
		{"listed", identity.Caller{ID: "k1", Restricted: true, Allow: []string{chatModel}}, nethttp.StatusOK},
		{"unlisted", identity.Caller{ID: "k1", Restricted: true, Allow: []string{"openai/gpt-4o"}}, nethttp.StatusForbidden},
		{"unrestricted", identity.Caller{ID: "k1"}, nethttp.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newChatRigFor(t, tc.key, 1<<20)
			rec := r.postTo(t.Context(), "/v1/messages/count_tokens", body, nil)
			if rec.Code != tc.want {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
			if tc.want == nethttp.StatusForbidden && !strings.Contains(rec.Body.String(), chatModel) {
				t.Fatalf("the refusal does not name the requested model: %s", rec.Body.String())
			}
			// Counting spends nothing: no provider was called whatever the answer.
			if calls := len(r.up.Calls()); calls != 0 {
				t.Fatalf("%d upstream calls", calls)
			}
		})
	}
}
