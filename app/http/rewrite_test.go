package http

import (
	nethttp "net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func rewritten(t *testing.T, target string) (path string, status int) {
	t.Helper()
	var got string
	h := Rewrite(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) { got = r.URL.Path }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(nethttp.MethodPost, target, nil))
	return got, rec.Code
}

func reaches(path, raw string) (reached bool, rec *httptest.ResponseRecorder) {
	req := httptest.NewRequest(nethttp.MethodGet, "/", nil)
	req.URL.Path, req.URL.RawPath = path, raw
	rec = httptest.NewRecorder()
	Rewrite(nethttp.HandlerFunc(func(nethttp.ResponseWriter, *nethttp.Request) { reached = true })).ServeHTTP(rec, req)
	return reached, rec
}

func TestRewriteTable(t *testing.T) {
	cases := map[string]string{
		"/v1/v1/messages":         "/v1/messages",
		"/v1/v1/chat/completions": "/v1/chat/completions",
		"/v1/v1":                  "/v1",
		"/codex":                  "/v1/responses",
		"/codex/responses":        "/v1/responses",
		"/codex/anything/deeper":  "/v1/responses",
		"/responses":              "/v1/responses",
		"/systemone":              "/v1/systemone",
		"/v1/chat/completions":    "/v1/chat/completions",
		"/v1beta/models":          "/v1beta/models",
		"/api/health":             "/api/health",
		"/":                       "/",
		"/v1/v1/v1/models":        "/v1/v1/models",
		"/v1/models/v1/x":         "/v1/models/v1/x",
	}
	for in, want := range cases {
		if got, status := rewritten(t, in); got != want || status != 200 {
			t.Errorf("%s -> %q (status %d), want %q", in, got, status, want)
		}
	}
}

func TestRewriteMatchesOnSegmentBoundaries(t *testing.T) {
	for _, p := range []string{"/codexfoo", "/codex-x/y", "/responsesx", "/responses/x", "/systemone2", "/systemone/x", "/v1/v1x/models", "/v1/v1beta"} {
		if got, _ := rewritten(t, p); got != p {
			t.Errorf("%s was rewritten to %s", p, got)
		}
	}
}

func TestRewriteOnlyOnce(t *testing.T) {
	if got, _ := rewritten(t, "/v1/v1/v1/v1/models"); got != "/v1/v1/v1/models" {
		t.Fatalf("got %q: a second pass would have collapsed it further", got)
	}
}

func TestRewriteClearsRawPathAndKeepsQuery(t *testing.T) {
	var path, raw, query string
	h := Rewrite(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		path, raw, query = r.URL.Path, r.URL.RawPath, r.URL.RawQuery
	}))
	req := httptest.NewRequest(nethttp.MethodGet, "/v1/v1/models?key=abc&x=1", nil)
	req.URL.RawPath = "/v1/v1/models"
	h.ServeHTTP(httptest.NewRecorder(), req)
	if path != "/v1/models" || raw != "" || query != "key=abc&x=1" {
		t.Fatalf("path %q raw %q query %q", path, raw, query)
	}
}

func TestRewriteDoesNotMutateTheCallersRequest(t *testing.T) {
	req := httptest.NewRequest(nethttp.MethodGet, "/responses", nil)
	Rewrite(nethttp.HandlerFunc(func(nethttp.ResponseWriter, *nethttp.Request) {})).ServeHTTP(httptest.NewRecorder(), req)
	if req.URL.Path != "/responses" {
		t.Fatalf("the original request was changed to %s", req.URL.Path)
	}
}

func TestQueryIsPreservedExactlyThroughAnAliasRewrite(t *testing.T) {
	var q, key string
	h := Rewrite(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		q, key = r.URL.RawQuery, r.URL.Query().Get("key")
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(nethttp.MethodGet, "/responses?key=abc%20d&a=1&a=2", nil))
	if q != "key=abc%20d&a=1&a=2" || key != "abc d" {
		t.Fatalf("query %q key %q", q, key)
	}
}

func TestNonCanonicalPathsNeverReachTheRouter(t *testing.T) {
	for _, target := range []string{
		"/v1/v1/../../api/settings", "/v1/./models", "/v1//models", "/v1/models/..", "/v1/messages/../models",
		"/api/../v1/models", "//v1/models", "/v1/models/",
	} {
		if got, status := rewritten(t, target); status != 400 || got != "" {
			t.Errorf("%s -> reached the router as %q with status %d", target, got, status)
		}
	}
}

func TestEncodedSlashAndDotSegmentsAreRejected(t *testing.T) {
	for _, target := range []string{"/v1/models%2f..%2fapi", "/v1/%2e%2e/api/settings", "/v1%2fmodels", "/api%2fhealth", "/v1/models%5c..", "/v1/%00models"} {
		decoded, err := url.PathUnescape(target)
		if err != nil {
			decoded = target
		}
		if reached, rec := reaches(decoded, target); reached || rec.Code != 400 {
			t.Errorf("%s: reached=%v status=%d", target, reached, rec.Code)
		}
	}
}

func TestEachDangerousCharacterIsRejectedOnItsOwn(t *testing.T) {
	cases := []struct{ name, path, raw string }{
		{"encoded slash", "/v1/a/b", "/v1/a%2fb"},
		{"encoded slash upper", "/v1/a/b", "/v1/a%2Fb"},
		{"encoded dot", "/v1/a.b", "/v1/a%2eb"},
		{"encoded dot upper", "/v1/a.b", "/v1/a%2Eb"},
		{"encoded backslash", "/v1/a\\b", "/v1/a%5cb"},
		{"encoded backslash upper", "/v1/a\\b", "/v1/a%5Cb"},
		{"encoded nul", "/v1/a\x00b", "/v1/a%00b"},
		{"raw backslash", "/v1/a\\b", ""},
		{"raw nul", "/v1/a\x00b", ""},
		{"raw tab", "/v1/a\tb", ""},
		{"raw newline", "/v1/a\nb", ""},
		{"raw del", "/v1/a\x7fb", ""},
		{"no leading slash", "v1/models", ""},
		{"empty path", "", ""},
		{"plain decoded path, encoded raw", "/v1/plain", "/v1/a%5Cb"},
	}
	for _, c := range cases {
		if reached, rec := reaches(c.path, c.raw); reached || rec.Code != nethttp.StatusBadRequest {
			t.Errorf("%s: reached=%v status=%d", c.name, reached, rec.Code)
		}
	}
}

func TestRejectedPathsAnswerInTheEndpointFormat(t *testing.T) {
	for path, want := range map[string]string{
		"/v1/messages/../x":   `"type":"error"`,
		"/v1beta/models/../x": `"status":"INVALID_ARGUMENT"`,
		"/v1/chat/../x":       `"code":"bad_request"`,
		"/api/../x":           `{"error":"`,
	} {
		_, rec := reaches(path, "")
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), want) || rec.Header().Get("Content-Type") != "application/json" {
			t.Errorf("%s: %d %s (want %s)", path, rec.Code, rec.Body.String(), want)
		}
	}
}

func TestRootAndPlainPathsPass(t *testing.T) {
	for _, p := range []string{"/", "/login", "/dashboard/home", "/api/health", "/v1/models", "/favicon.ico", "/a.b/c-d_e~f"} {
		if got, status := rewritten(t, p); got != p || status != 200 {
			t.Errorf("%s -> %q %d", p, got, status)
		}
	}
}

func TestPathologicalPathsDoNotPanic(t *testing.T) {
	for _, p := range []string{"", "x", "*", "/*", "/%", "/v1/v1/", "/codex/", "/ ", "/v1/\x7f"} {
		req := httptest.NewRequest(nethttp.MethodOptions, "/", nil)
		req.URL.Path = p
		Rewrite(nethttp.HandlerFunc(func(nethttp.ResponseWriter, *nethttp.Request) {})).ServeHTTP(httptest.NewRecorder(), req)
	}
}
