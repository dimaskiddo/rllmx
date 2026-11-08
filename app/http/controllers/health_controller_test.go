package controllers

import (
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apphttp "rllmx/app/http"
	"rllmx/tests"
)

func newGuardedHealth(t *testing.T) nethttp.Handler {
	t.Helper()
	r := tests.NewRoute(t)
	r.SetGlobalMiddleware(nil)
	g := apphttp.NewGuard(nil, true)
	g.Handle(r, apphttp.ClassPublic, "GET", "/api/health", NewHealthController().Index)
	return r
}

func TestHealthAnswersOkTrue(t *testing.T) {
	h := newGuardedHealth(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(nethttp.MethodGet, "/api/health", nil))
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"ok":true}` {
		t.Fatalf("%d %q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content type %q", ct)
	}
}

func TestHealthOnHeadSendsNoBody(t *testing.T) {
	h := newGuardedHealth(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(nethttp.MethodHead, "/api/health", nil))
	if rec.Code != 200 || rec.Body.Len() != 0 {
		t.Fatalf("%d %q", rec.Code, rec.Body.String())
	}
}

func TestHealthRevealsNothingAboutTheDeployment(t *testing.T) {
	h := newGuardedHealth(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(nethttp.MethodGet, "/api/health", nil))
	if len(rec.Body.String()) > 20 {
		t.Fatalf("the body says more than ok: %q", rec.Body.String())
	}
}
