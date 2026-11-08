package middleware

import (
	"io"
	nethttp "net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	contractshttp "github.com/goravel/framework/contracts/http"
	goravelgin "github.com/goravel/gin"

	"rllmx/app/failure"
	apphttp "rllmx/app/http"
)

// send issues one request with an optional body and socket peer and returns the
// recorded answer.
func send(r *goravelgin.Route, method, target string, hdr map[string]string, body string, peer ...string) *httptest.ResponseRecorder {
	remote := "203.0.113.9:5000"
	if len(peer) > 0 {
		remote = peer[0]
	}
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, rd)
	req.RemoteAddr = remote
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// getOnly installs mw in front of a route that answers 200 "reached" and asks
// it once, so a test can see the headers and whether the handler ran.
func getOnly(t *testing.T, path string, mw contractshttp.Middleware, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := newRoute(t, mw)
	r.Get(path, reached)
	return send(r, nethttp.MethodGet, path, hdr, "")
}

func TestSecurityHeadersOnEveryResponse(t *testing.T) {
	cases := []struct {
		path    string
		csp     bool
		noStore bool
	}{
		{path: "/api/thing", noStore: true},
		{path: "/v1/chat/completions"},
		{path: "/", csp: true},
		{path: "/dashboard/home", csp: true},
	}
	for _, tc := range cases {
		rec := getOnly(t, tc.path, &SecurityHeaders{}, nil)
		if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s nosniff = %q", tc.path, got)
		}
		if got := rec.Header().Get("Cache-Control") == "no-store"; got != tc.noStore {
			t.Errorf("%s Cache-Control no-store = %v", tc.path, got)
		}
		if got := rec.Header().Get("Content-Security-Policy") != ""; got != tc.csp {
			t.Errorf("%s CSP present = %v", tc.path, got)
		}
		if got := rec.Header().Get("Referrer-Policy") != ""; got != tc.csp {
			t.Errorf("%s Referrer-Policy present = %v", tc.path, got)
		}
		if got := rec.Header().Get("Strict-Transport-Security"); got != "" {
			t.Errorf("%s HSTS %q over plain HTTP", tc.path, got)
		}
	}
}

func TestSecurityHeadersSendHSTSOnlyWhenTheCallerArrivedOverHTTPS(t *testing.T) {
	proxy := apphttp.NewTrust([]netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")})
	cases := []struct {
		name  string
		trust apphttp.Trust
		peer  string
		hdr   map[string]string
		want  bool
	}{
		{name: "plain http", trust: apphttp.NewTrust(nil), peer: "127.0.0.1:1"},
		{name: "trusted proxy says https", trust: proxy, peer: "10.1.2.3:1", hdr: map[string]string{"X-Forwarded-Proto": "https"}, want: true},
		{name: "an untrusted peer cannot claim it", trust: proxy, peer: "203.0.113.9:1", hdr: map[string]string{"X-Forwarded-Proto": "https"}},
	}
	for _, tc := range cases {
		r := newRoute(t, &SecurityHeaders{Trust: tc.trust})
		r.Get("/api/thing", reached)
		rec := send(r, nethttp.MethodGet, "/api/thing", tc.hdr, "", tc.peer)
		if got := rec.Header().Get("Strict-Transport-Security"); (got != "") != tc.want {
			t.Errorf("%s: HSTS %q", tc.name, got)
		}
	}
}

func TestCORSIsOnlyOnTheClientAPI(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{path: "/v1/chat/completions", want: true},
		{path: "/v1beta/models/x:generateContent", want: true},
		{path: "/api/settings"},
		{path: "/"},
	}
	for _, tc := range cases {
		rec := getOnly(t, tc.path, &CORS{}, nil)
		if got := rec.Header().Get("Access-Control-Allow-Origin") == "*"; got != tc.want {
			t.Errorf("%s allow-origin = %v", tc.path, got)
		}
		if tc.want {
			if got := rec.Header().Get("Access-Control-Expose-Headers"); !strings.Contains(got, "X-Request-Id") || !strings.Contains(got, "Retry-After") || !strings.Contains(got, "X-Rllm-Cache") {
				t.Errorf("%s expose = %q", tc.path, got)
			}
		}
		if got := rec.Header().Get("Access-Control-Allow-Methods"); got != "" {
			t.Errorf("%s a plain GET carried preflight headers: %q", tc.path, got)
		}
	}
}

func TestCORSPreflightIsAnsweredBeforeTheHandler(t *testing.T) {
	r := newRoute(t, &CORS{})
	r.Options("/v1/chat/completions", reached)
	rec := send(r, nethttp.MethodOptions, "/v1/chat/completions", map[string]string{
		"Origin":                         "https://app.example",
		"Access-Control-Request-Method":  "POST",
		"Access-Control-Request-Headers": "authorization, x-custom",
	}, "")
	if rec.Code != nethttp.StatusNoContent {
		t.Fatalf("preflight = %d, want 204", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "reached") {
		t.Error("the handler ran for a preflight")
	}
	for name, want := range map[string]string{
		"Access-Control-Allow-Methods": "POST",
		"Access-Control-Allow-Headers": "authorization, x-custom",
		"Access-Control-Max-Age":       "86400",
	} {
		if got := rec.Header().Get(name); !strings.Contains(got, want) {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if got := rec.Header().Get("Vary"); got != "Access-Control-Request-Headers" {
		t.Errorf("Vary = %q", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("credentials allowed: %q", got)
	}
}

func TestCORSPreflightReplacesAHostileHeaderList(t *testing.T) {
	r := newRoute(t, &CORS{})
	r.Options("/v1/messages", reached)
	rec := send(r, nethttp.MethodOptions, "/v1/messages", map[string]string{
		"Access-Control-Request-Method":  "POST",
		"Access-Control-Request-Headers": "x\r\nInjected: 1",
	}, "")
	if got := rec.Header().Get("Access-Control-Allow-Headers"); strings.Contains(strings.ToLower(got), "injected") || strings.ContainsAny(got, "\r\n") {
		t.Fatalf("echoed a forged header list: %q", got)
	}
}

func TestOptionsWithoutAMethodStillReachesTheGuard(t *testing.T) {
	g := apphttp.NewGuard(nil, true)
	r := newRoute(t, &CORS{}, &Guard{Guard: g})
	g.Handle(r, apphttp.ClassLLM, "POST", "/v1/chat/completions", reached)
	rec := send(r, nethttp.MethodOptions, "/v1/chat/completions", nil, "")
	if rec.Code != 401 || strings.Contains(rec.Body.String(), "reached") {
		t.Fatalf("%d %q: a bare OPTIONS must go through the guard", rec.Code, rec.Body.String())
	}
}

func TestBodyLimitRefusesADeclaredOversizeBodyInThePathFormat(t *testing.T) {
	read := func(ctx contractshttp.Context) contractshttp.Response {
		_, err := io.ReadAll(ctx.Request().Origin().Body)
		if err != nil {
			return ctx.Response().String(failure.ReadStatus(err), "stopped")
		}
		return ctx.Response().String(200, "read")
	}
	r := newRoute(t, &BodyLimit{ClientMax: 1024})
	r.Post("/v1/chat/completions", read)
	r.Post("/api/settings", read)

	rec := send(r, nethttp.MethodPost, "/v1/chat/completions", nil, strings.Repeat("x", 1025))
	if rec.Code != nethttp.StatusRequestEntityTooLarge {
		t.Fatalf("declared oversize = %d", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, `"error"`) || strings.Contains(body, "read") {
		t.Fatalf("body %q: not the endpoint's error shape", body)
	}

	if rec := send(r, nethttp.MethodPost, "/v1/chat/completions", nil, strings.Repeat("x", 100)); rec.Code != 200 {
		t.Fatalf("a body under the cap = %d", rec.Code)
	}
	if rec := send(r, nethttp.MethodPost, "/api/settings", nil, strings.Repeat("x", 1025)); rec.Code != 200 {
		t.Fatalf("the /api cap must not be the client one: %d", rec.Code)
	}
}

func TestBodyLimitStopsABodyWithNoDeclaredLengthAtTheCap(t *testing.T) {
	var got int
	read := func(ctx contractshttp.Context) contractshttp.Response {
		b, err := io.ReadAll(ctx.Request().Origin().Body)
		got = len(b)
		if err != nil {
			return ctx.Response().String(failure.ReadStatus(err), "stopped")
		}
		return ctx.Response().String(200, "read")
	}
	r := newRoute(t, &BodyLimit{ClientMax: 16})
	r.Post("/v1/messages", read)

	// ContentLength -1 is a body whose length the client never declared, which
	// is the case a declared-length check alone cannot cover.
	req := httptest.NewRequest(nethttp.MethodPost, "/v1/messages", strings.NewReader(strings.Repeat("y", 1<<16)))
	req.ContentLength = -1
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != nethttp.StatusRequestEntityTooLarge || got > 16 {
		t.Fatalf("%d read=%d: the read was not cut at the cap", rec.Code, got)
	}
}

func TestSameOriginDecisionTable(t *testing.T) {
	cases := []struct {
		name string
		hdr  map[string]string
		want int
	}{
		{"same-origin", map[string]string{"Sec-Fetch-Site": "same-origin"}, 200},
		{"none", map[string]string{"Sec-Fetch-Site": "none"}, 200},
		{"cross-site", map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
		{"same-site is refused", map[string]string{"Sec-Fetch-Site": "same-site"}, 403},
		{"origin equals host", map[string]string{"Origin": "http://localhost:20128"}, 200},
		{"foreign origin", map[string]string{"Origin": "https://evil.example"}, 403},
		{"neither header is a script", nil, 200},
	}
	for _, tc := range cases {
		r := newRoute(t, &SameOrigin{})
		r.Post("/api/write", reached)
		req := httptest.NewRequest(nethttp.MethodPost, "/api/write", nil)
		req.Host = "localhost:20128"
		for k, v := range tc.hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("%s = %d, want %d", tc.name, rec.Code, tc.want)
		}
	}
}

func TestSameOriginRefusesARepeatedHeader(t *testing.T) {
	r := newRoute(t, &SameOrigin{})
	r.Post("/api/write", reached)
	req := httptest.NewRequest(nethttp.MethodPost, "/api/write", nil)
	req.Header.Add("Sec-Fetch-Site", "same-origin")
	req.Header.Add("Sec-Fetch-Site", "cross-site")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != nethttp.StatusForbidden {
		t.Fatalf("%d: a repeated header was trusted", rec.Code)
	}
}

func TestSameOriginLeavesTheClientAPIAndSafeMethodsAlone(t *testing.T) {
	for _, tc := range []struct{ method, path string }{
		{nethttp.MethodPost, "/v1/chat/completions"},
		{nethttp.MethodGet, "/api/thing"},
		{nethttp.MethodPost, "/api/auth/saml/acs"},
	} {
		r := newRoute(t, &SameOrigin{Exempt: []string{"/api/auth/saml/acs"}})
		r.Post(tc.path, reached)
		r.Get(tc.path, reached)
		rec := send(r, tc.method, tc.path, map[string]string{"Sec-Fetch-Site": "cross-site"}, "")
		if rec.Code != 200 {
			t.Errorf("%s %s = %d", tc.method, tc.path, rec.Code)
		}
	}
}

func TestCORSAndSecurityHeadersSurviveAGuardDenial(t *testing.T) {
	g := apphttp.NewGuard(nil, true)
	r := newRoute(t, &SecurityHeaders{}, &CORS{}, &Guard{Guard: g})
	g.Handle(r, apphttp.ClassLLM, "POST", "/v1/chat/completions", reached)
	g.Handle(r, apphttp.ClassProtected, "GET", "/api/settings", reached)

	rec := send(r, nethttp.MethodPost, "/v1/chat/completions", nil, "")
	if rec.Code != 401 {
		t.Fatalf("%d", rec.Code)
	}
	for _, name := range []string{"Access-Control-Allow-Origin", "Access-Control-Expose-Headers", "X-Content-Type-Options"} {
		if rec.Header().Get(name) == "" {
			t.Errorf("a denied client API call is missing %s", name)
		}
	}

	rec = send(r, nethttp.MethodGet, "/api/settings", nil, "")
	if rec.Code != 401 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("a denied dashboard call: %d cache=%q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("the dashboard API sent CORS %q", got)
	}
}

func TestSameOriginListsOnlyTheSAMLACSAsExempt(t *testing.T) {
	// The stack passes exactly this list; a path added here would be a
	// cross-site write the product accepts without a fetch-metadata check.
	m := &SameOrigin{Exempt: []string{samlACS}}
	if !m.exempt(samlACS) {
		t.Fatal("the SAML ACS is not exempt")
	}
	for _, p := range []string{"/api/settings", "/api/auth/saml/acs/", "/api/auth/saml/acsx"} {
		if m.exempt(p) {
			t.Errorf("%s is exempt and must not be", p)
		}
	}
}
