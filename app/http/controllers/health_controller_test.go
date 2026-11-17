package controllers

import (
	"bufio"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"rllmx/app/access/auth"
	"rllmx/app/gateway/health"
	apphttp "rllmx/app/http"
	"rllmx/app/store/repository"
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

func TestTheLatencyFeedSendsItsTableThenKeepAlives(t *testing.T) {
	m := newMgmtRig(t)
	tr := health.NewTracker(time.Now)
	tr.Record("openai", true, 120*time.Millisecond)
	hc := &HealthController{
		DB:      func() *repository.Store { return m.st.Store },
		Tracker: tr,
		every:   10 * time.Millisecond,
		beat:    40 * time.Millisecond,
	}
	m.guard.Handle(m.router, apphttp.ClassProtected, nethttp.MethodGet, "/api/health/latency-stream", hc.LatencyStream)
	srv := httptest.NewServer(m.route)
	defer srv.Close()

	req, err := nethttp.NewRequest(nethttp.MethodGet, srv.URL+"/api/health/latency-stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&nethttp.Cookie{Name: auth.CookieName, Value: m.cookie})
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("%d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}

	br := bufio.NewReader(resp.Body)
	first, err := br.ReadString('\n')
	if err != nil || !strings.HasPrefix(first, "data: ") || !strings.Contains(first, `"openai"`) {
		t.Fatalf("first frame %q (%v)", first, err)
	}
	if !strings.Contains(first, `"emaLatency":120`) {
		t.Fatalf("the table is missing from the frame: %s", first)
	}

	beat := false
	for i := 0; i < 40 && !beat; i++ {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("the feed ended before a keepalive: %v", err)
		}
		beat = strings.HasPrefix(line, ": keepalive")
	}
	if !beat {
		t.Fatal("no keepalive arrived")
	}
}

func TestTheLatencyFeedNeedsASession(t *testing.T) {
	m := newMgmtRig(t)
	hc := &HealthController{DB: func() *repository.Store { return m.st.Store }}
	m.guard.Handle(m.router, apphttp.ClassProtected, nethttp.MethodGet, "/api/health/latency-stream", hc.LatencyStream)
	srv := httptest.NewServer(m.route)
	defer srv.Close()
	resp, err := srv.Client().Get(srv.URL + "/api/health/latency-stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != nethttp.StatusUnauthorized {
		t.Fatalf("%d for a request with no session", resp.StatusCode)
	}
}
