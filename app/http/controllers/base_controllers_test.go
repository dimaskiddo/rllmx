package controllers

import (
	"context"
	"errors"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/goravel/framework/contracts/http"

	"rllmx/tests"
)

func serve(t *testing.T, path string, handler http.HandlerFunc, method string) *httptest.ResponseRecorder {
	t.Helper()
	r := tests.NewRoute(t)
	r.SetGlobalMiddleware(nil)
	r.Get(path, handler)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, nil)
	r.ServeHTTP(rec, req)
	return rec
}

func TestVersionCarriesTheBuildAndNoRegistryLookup(t *testing.T) {
	rec := serve(t, "/api/version", NewVersionController("1.2.3", "abc").Index, nethttp.MethodGet)
	if rec.Code != 200 {
		t.Fatalf("%d", rec.Code)
	}
	body := strings.TrimSpace(rec.Body.String())
	for _, want := range []string{`"currentVersion":"1.2.3"`, `"commit":"abc"`, `"latestVersion":null`, `"hasUpdate":false`} {
		if !strings.Contains(body, want) {
			t.Errorf("%s is missing from %s", want, body)
		}
	}
	if rec := serve(t, "/api/version", NewVersionController("1.2.3", "abc").Index, nethttp.MethodHead); rec.Body.Len() != 0 || rec.Code != 200 {
		t.Fatalf("HEAD: %d %q", rec.Code, rec.Body.String())
	}
}

func TestInitNeedsSetupOnlyForAnEmptyStore(t *testing.T) {
	for _, tc := range []struct {
		name  string
		count func(context.Context) (int64, error)
		want  string
	}{
		{name: "empty", count: func(context.Context) (int64, error) { return 0, nil }, want: `"needsSetup":true`},
		{name: "configured", count: func(context.Context) (int64, error) { return 2, nil }, want: `"needsSetup":false`},
		{name: "unreadable", count: func(context.Context) (int64, error) { return 0, errors.New("closed") }, want: `"needsSetup":false`},
		{name: "no counter", count: nil, want: `"needsSetup":false`},
	} {
		rec := serve(t, "/api/init", NewInitController(tc.count).Index, nethttp.MethodGet)
		body := strings.TrimSpace(rec.Body.String())
		if rec.Code != 200 || !strings.Contains(body, `"initialized":true`) || !strings.Contains(body, tc.want) {
			t.Errorf("%s: %d %q", tc.name, rec.Code, body)
		}
	}
}
