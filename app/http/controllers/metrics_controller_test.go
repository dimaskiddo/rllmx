package controllers

import (
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	contractshttp "github.com/goravel/framework/contracts/http"

	"rllmx/app/access/auth"
	"rllmx/app/gateway/metrics"
	apphttp "rllmx/app/http"
	"rllmx/app/store/analytics"
	"rllmx/tests"
)

// sampleRecord is one priced chat request the scrape then counts.
func sampleRecord() analytics.Record {
	return analytics.Record{
		Provider: "openai", Model: "gpt-5", ConnectionID: "c1", APIKeyID: "k1", Endpoint: "/v1/chat/completions",
		Status: 200, PromptTokens: 10, CompletionTokens: 5, CostKnown: true, Cost: 0.01, LatencyMs: 800,
	}
}

const (
	testToken = "0123456789abcdef0123456789abcdef"
	testPass  = "0123456789abcdef"
)

func metricsHandler(t *testing.T, c *MetricsController) nethttp.Handler {
	t.Helper()
	r := tests.NewRoute(t)
	// The stock set reads configuration this bare route does not have.
	r.SetGlobalMiddleware(nil)
	r.Get("/metrics", func(ctx contractshttp.Context) contractshttp.Response { return c.Index(ctx) })
	return r
}

func scrape(handler nethttp.Handler, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(nethttp.MethodGet, "/metrics", nil)
	req.RemoteAddr = "203.0.113.9:5000"
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestTheScrapeRefusesAnUnsetOrWrongCredential(t *testing.T) {
	reg := metrics.New(nil)
	reg.Observe(sampleRecord())
	bearer := metricsHandler(t, &MetricsController{Reg: reg, Token: testToken, Trust: apphttp.NewTrust(nil)})
	basic := metricsHandler(t, &MetricsController{Reg: reg, Username: "prom", Password: testPass, Trust: apphttp.NewTrust(nil)})
	cases := []struct {
		name    string
		handler nethttp.Handler
		hdr     map[string]string
		want    int
	}{
		{"no credential configured", metricsHandler(t, &MetricsController{Reg: reg, Trust: apphttp.NewTrust(nil)}), nil, nethttp.StatusUnauthorized},
		{"no header", bearer, nil, nethttp.StatusUnauthorized},
		{"wrong token", bearer, map[string]string{"Authorization": "Bearer nope"}, nethttp.StatusUnauthorized},
		{"right token", bearer, map[string]string{"Authorization": "Bearer " + testToken}, nethttp.StatusOK},
		{"basic against a token-only route", bearer, basicAuth("prom", testPass), nethttp.StatusUnauthorized},
		{"bearer against a basic-only route", basic, map[string]string{"Authorization": "Bearer " + testToken}, nethttp.StatusUnauthorized},
		{"right pair", basic, basicAuth("prom", testPass), nethttp.StatusOK},
		{"wrong user", basic, basicAuth("other", testPass), nethttp.StatusUnauthorized},
		{"wrong password", basic, basicAuth("prom", "nope-nope-nope-nope"), nethttp.StatusUnauthorized},
	}
	for _, tc := range cases {
		rec := scrape(tc.handler, tc.hdr)
		if rec.Code != tc.want {
			t.Errorf("%s: %d %s", tc.name, rec.Code, rec.Body.String())
		}
	}
}

func basicAuth(user, pass string) map[string]string {
	req := httptest.NewRequest(nethttp.MethodGet, "/", nil)
	req.SetBasicAuth(user, pass)
	return map[string]string{"Authorization": req.Header.Get("Authorization")}
}

func TestAnAcceptedScrapeIsOpenMetricsTextThatEnds(t *testing.T) {
	reg := metrics.New(nil)
	reg.Observe(sampleRecord())
	rec := scrape(metricsHandler(t, &MetricsController{Reg: reg, Token: testToken, Trust: apphttp.NewTrust(nil)}),
		map[string]string{"Authorization": "Bearer " + testToken})
	if rec.Code != nethttp.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/openmetrics-text") {
		t.Errorf("content type %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("cache control %q", got)
	}
	if !strings.HasSuffix(rec.Body.String(), "# EOF\n") {
		t.Errorf("the scrape does not end with # EOF")
	}
}

func TestTheRefusalNamesTheBasicRealmAndCountsTowardTheLock(t *testing.T) {
	lim := auth.NewLimiter()
	c := &MetricsController{Reg: metrics.New(nil), Username: "prom", Password: testPass, Limiter: lim, Trust: apphttp.NewTrust(nil)}
	h := metricsHandler(t, c)
	first := scrape(h, basicAuth("prom", "wrong-password-x"))
	if first.Code != nethttp.StatusUnauthorized || first.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("%d %q", first.Code, first.Header().Get("WWW-Authenticate"))
	}
	for i := 0; i < auth.FreeAttempts+1; i++ {
		scrape(h, basicAuth("prom", "wrong-password-x"))
	}
	if rec := scrape(h, basicAuth("prom", "wrong-password-x")); rec.Code != nethttp.StatusTooManyRequests {
		t.Fatalf("a guessing address was not locked: %d", rec.Code)
	}
}

func TestAScrapeNeitherClearsNorSpendsTheSignInBudget(t *testing.T) {
	lim := auth.NewLimiter()
	h := metricsHandler(t, &MetricsController{Reg: metrics.New(nil), Token: testToken, Limiter: lim, Trust: apphttp.NewTrust(nil)})
	now := time.Now()
	key := auth.Key("203.0.113.9:5000")
	for i := 0; i < auth.FreeAttempts-1; i++ {
		lim.Fail(key, now)
	}
	if rec := scrape(h, map[string]string{"Authorization": "Bearer " + testToken}); rec.Code != nethttp.StatusOK {
		t.Fatalf("%d", rec.Code)
	}
	// One more failed sign-in locks the address only if the scrape left the
	// earlier failures standing.
	if _, locked := lim.Fail(key, now); locked == 0 {
		t.Fatal("a good scrape wiped the password sign-in's failures")
	}

	fresh := auth.NewLimiter()
	h = metricsHandler(t, &MetricsController{Reg: metrics.New(nil), Token: testToken, Limiter: fresh, Trust: apphttp.NewTrust(nil)})
	for i := 0; i < auth.FreeAttempts+1; i++ {
		scrape(h, map[string]string{"Authorization": "Bearer wrong"})
	}
	if fresh.Locked(key, now) != 0 {
		t.Fatal("bad scrapes locked the address's password sign-in")
	}
	if fresh.Locked("metrics:"+key, now) == 0 {
		t.Fatal("bad scrapes were not counted against the scrape itself")
	}
}
