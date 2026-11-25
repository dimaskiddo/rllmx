package controllers

import (
	"context"
	"errors"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/goravel/framework/contracts/http"

	"rllmx/tests"
)

// servePost registers one POST route, which serve() keeps to GET.
func servePost(t *testing.T, path string, handler http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	r := tests.NewRoute(t)
	r.SetGlobalMiddleware(nil)
	r.Post(path, handler)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(nethttp.MethodPost, path, nil))
	return rec
}

func TestTheAboutUpdateCheckComparesTheNewestRelease(t *testing.T) {
	release := []byte(`{"tag_name":"v0.6.0","html_url":"https://github.com/dimaskiddo/rllmx/releases/tag/v0.6.0","published_at":"2026-11-20T10:00:00Z"}`)
	fetch := func(context.Context, string) ([]byte, error) { return release, nil }
	for _, tc := range []struct {
		name    string
		version string
		want    string
	}{
		{name: "same", version: "0.6.0", want: `"hasUpdate":false`},
		{name: "older", version: "0.5.0", want: `"hasUpdate":true`},
		{name: "unversioned build", version: "dev", want: `"hasUpdate":false`},
	} {
		c := &AboutController{Version: tc.version, Fetch: fetch}
		rec := serve(t, "/api/about/update", c.Update, nethttp.MethodGet)
		body := strings.TrimSpace(rec.Body.String())
		if rec.Code != 200 || !strings.Contains(body, tc.want) {
			t.Errorf("%s: %d %q", tc.name, rec.Code, body)
		}
		for _, want := range []string{`"latestVersion":"0.6.0"`, `"url":"https://github.com/dimaskiddo/rllmx/releases/tag/v0.6.0"`, `"publishedAt":"2026-11-20T10:00:00Z"`} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: %s is missing from %s", tc.name, want, body)
			}
		}
	}
}

func TestTheAboutUpdateCheckHidesItsFailures(t *testing.T) {
	failing := func(context.Context, string) ([]byte, error) { return nil, errors.New("dial 10.0.0.1: refused") }
	garbled := func(context.Context, string) ([]byte, error) { return []byte("not json"), nil }
	for _, tc := range []struct {
		name  string
		fetch func(context.Context, string) ([]byte, error)
		code  int
	}{
		{name: "no client", fetch: nil, code: nethttp.StatusServiceUnavailable},
		{name: "unreachable", fetch: failing, code: nethttp.StatusBadGateway},
		{name: "unreadable answer", fetch: garbled, code: nethttp.StatusBadGateway},
	} {
		c := &AboutController{Version: "0.5.0", Fetch: tc.fetch}
		rec := serve(t, "/api/about/update", c.Update, nethttp.MethodGet)
		body := rec.Body.String()
		if rec.Code != tc.code {
			t.Errorf("%s: %d %q", tc.name, rec.Code, body)
		}
		if strings.Contains(body, "10.0.0.1") || strings.Contains(body, "not json") {
			t.Errorf("%s: the answer carries the failure: %s", tc.name, body)
		}
	}
}

func TestTheAboutUpgradeIsOffUnlessWired(t *testing.T) {
	c := &AboutController{Version: "0.5.0"}
	rec := servePost(t, "/api/about/upgrade", c.RunUpgrade)
	if rec.Code != nethttp.StatusServiceUnavailable {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestTheAboutUpgradeAnswersBeforeItRestarts(t *testing.T) {
	restarted := make(chan string, 1)
	c := &AboutController{
		Version: "0.5.0",
		Upgrade: func(context.Context) (string, string, error) { return "0.6.0", "/opt/rllmx", nil },
		Restart: func(exe string) { restarted <- exe },
	}
	rec := servePost(t, "/api/about/upgrade", c.RunUpgrade)
	if rec.Code != nethttp.StatusOK || !strings.Contains(rec.Body.String(), `"latestVersion":"0.6.0"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	select {
	case exe := <-restarted:
		if exe != "/opt/rllmx" {
			t.Fatalf("restarted %q, want the path the install returned", exe)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the process was never restarted")
	}
	c.Upgrade = func(context.Context) (string, string, error) { return "", "", errors.New("checksum") }
	rec = servePost(t, "/api/about/upgrade", c.RunUpgrade)
	if rec.Code != nethttp.StatusBadGateway || strings.Contains(rec.Body.String(), "checksum") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestASecondUpgradeWhileOneRunsIsRefused(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	c := &AboutController{
		Version: "0.5.0",
		Upgrade: func(context.Context) (string, string, error) {
			close(started)
			<-release
			return "0.6.0", "/opt/rllmx", nil
		},
		Restart: func(string) {},
	}
	done := make(chan int, 1)
	go func() { done <- servePost(t, "/api/about/upgrade", c.RunUpgrade).Code }()
	<-started
	if rec := servePost(t, "/api/about/upgrade", c.RunUpgrade); rec.Code != nethttp.StatusConflict {
		t.Fatalf("second install: %d %s", rec.Code, rec.Body.String())
	}
	close(release)
	if code := <-done; code != nethttp.StatusOK {
		t.Fatalf("first install: %d", code)
	}
}
