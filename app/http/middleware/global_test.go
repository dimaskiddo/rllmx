package middleware

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	contractshttp "github.com/goravel/framework/contracts/http"
	goravelgin "github.com/goravel/gin"

	apphttp "rllmx/app/http"
	"rllmx/tests"
)

func reached(ctx contractshttp.Context) contractshttp.Response {
	return ctx.Response().String(200, "reached")
}

func newRoute(t *testing.T, mws ...contractshttp.Middleware) *goravelgin.Route {
	t.Helper()
	r := tests.NewRoute(t)
	r.SetGlobalMiddleware(mws)
	return r
}

func get(r *goravelgin.Route, target string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.RemoteAddr = "203.0.113.9:5000"
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

var hex32 = regexp.MustCompile(`^[0-9a-f]{32}$`)

func TestRequestIdIsGeneratedAndReturned(t *testing.T) {
	r := newRoute(t, &RequestID{})
	r.Get("/rid/a", reached)
	id := get(r, "/rid/a", nil).Header().Get("X-Request-Id")
	if !hex32.MatchString(id) {
		t.Fatalf("generated id = %q", id)
	}
	if other := get(r, "/rid/a", nil).Header().Get("X-Request-Id"); other == id {
		t.Fatal("two requests got the same id")
	}
}

func TestRequestIdAcceptsOnlySafeValues(t *testing.T) {
	r := newRoute(t, &RequestID{})
	r.Get("/rid/b", reached)
	for _, ok := range []string{"abc-123", "A.b_c-9", strings.Repeat("a", 64)} {
		if got := get(r, "/rid/b", map[string]string{"X-Request-Id": ok}).Header().Get("X-Request-Id"); got != ok {
			t.Errorf("%q was replaced by %q", ok, got)
		}
	}
	for _, bad := range []string{strings.Repeat("a", 65), "has space", "semi;colon", "new\tline", "über", "a/b", "<script>"} {
		got := get(r, "/rid/b", map[string]string{"X-Request-Id": bad}).Header().Get("X-Request-Id")
		if !hex32.MatchString(got) {
			t.Errorf("%q was kept as %q", bad, got)
		}
	}
}

func TestRequestIdReachesTheHandlerAndTheErrorResponses(t *testing.T) {
	var seen string
	r := newRoute(t, &RequestID{})
	r.Get("/rid/c", func(ctx contractshttp.Context) contractshttp.Response {
		seen = RequestIDFrom(ctx)
		return ctx.Response().String(200, "ok")
	})
	rec := get(r, "/rid/c", map[string]string{"X-Request-Id": "trace-7"})
	if seen != "trace-7" || rec.Header().Get("X-Request-Id") != "trace-7" {
		t.Fatalf("handler saw %q, header %q", seen, rec.Header().Get("X-Request-Id"))
	}
	if RequestIDFrom(nil) != "" {
		t.Fatal("no context must give no id")
	}
}

func TestAccessLogHasPathStatusAndIdButNeverTheQueryOrSecrets(t *testing.T) {
	log := &tests.Log{}
	r := newRoute(t, &RequestID{}, &AccessLog{Log: log})
	r.Get("/log/a", reached)
	get(r, "/log/a?key=sk-secret-key&x=1", map[string]string{
		"Authorization": "Bearer sk-secret-token", "Cookie": "auth_token=cookie-secret", "X-Request-Id": "req-1",
	})
	lines := log.Lines()
	if len(lines) != 1 {
		t.Fatalf("lines = %q", lines)
	}
	line := lines[0]
	for _, want := range []string{"GET", "/log/a", "200", "req-1"} {
		if !strings.Contains(line, want) {
			t.Errorf("%q is missing from %q", want, line)
		}
	}
	for _, leak := range []string{"sk-secret-key", "sk-secret-token", "cookie-secret", "key=", "x=1", "?"} {
		if strings.Contains(line, leak) {
			t.Errorf("%q leaked into %q", leak, line)
		}
	}
}

func TestAccessLogRecordsAnUnroutedRequestAndItsStatus(t *testing.T) {
	log := &tests.Log{}
	r := newRoute(t, &AccessLog{Log: log})
	r.Fallback(func(ctx contractshttp.Context) contractshttp.Response {
		return ctx.Response().String(404, "gone")
	})
	get(r, "/nowhere", nil)
	if lines := log.Lines(); len(lines) != 1 || !strings.Contains(lines[0], "/nowhere") || !strings.Contains(lines[0], "404") {
		t.Fatalf("lines = %q", lines)
	}
}

func TestAccessLogCleansControlCharactersInThePath(t *testing.T) {
	log := &tests.Log{}
	r := newRoute(t, &AccessLog{Log: log})
	r.Get("/log/{name}", reached)
	get(r, "/log/a%0AINFO%20forged%0D%00line", nil)
	lines := log.Lines()
	if len(lines) != 1 || strings.ContainsAny(lines[0], "\r\n\x00") {
		t.Fatalf("lines = %q", lines)
	}
}

func TestAccessLogBoundsALongPath(t *testing.T) {
	log := &tests.Log{}
	r := newRoute(t, &AccessLog{Log: log})
	r.Get("/log/{name}", reached)
	get(r, "/log/"+strings.Repeat("a", 5000), nil)
	if lines := log.Lines(); len(lines) != 1 || len(lines[0]) > 1000 {
		t.Fatalf("a long path was logged whole: %d bytes", len(lines[0]))
	}
}

func TestAccessLogSeesTheStatusAGuardDenialWrote(t *testing.T) {
	log := &tests.Log{}
	g := apphttp.NewGuard(nil, true)
	r := newRoute(t, &RequestID{}, &AccessLog{Log: log}, &Guard{Guard: g})
	r.Fallback(g.Fallback(r.GetRoutes))
	g.Handle(r, apphttp.ClassProtected, "GET", "/api/log-denied", reached)
	rec := get(r, "/api/log-denied", nil)
	if rec.Code != 401 {
		t.Fatalf("status %d", rec.Code)
	}
	if lines := log.Lines(); len(lines) != 1 || !strings.Contains(lines[0], "401") {
		t.Fatalf("lines = %q", lines)
	}
}

func TestGuardMiddlewareStopsTheChainOnADenial(t *testing.T) {
	g := apphttp.NewGuard(nil, true)
	r := newRoute(t, &Guard{Guard: g})
	g.Handle(r, apphttp.ClassProtected, "GET", "/api/guarded", reached)
	rec := get(r, "/api/guarded", nil)
	if rec.Code != 401 || strings.Contains(rec.Body.String(), "reached") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	g.Handle(r, apphttp.ClassPublic, "GET", "/api/open", reached)
	if rec := get(r, "/api/open", nil); rec.Code != 200 {
		t.Fatalf("a public route: %d", rec.Code)
	}
}

func TestSignaturesAreDistinct(t *testing.T) {
	seen := map[string]bool{}
	for _, m := range []contractshttp.Middleware{
		&RequestID{}, &AccessLog{}, &SecurityHeaders{}, &CORS{},
		&Deadlines{}, &BodyLimit{}, &SameOrigin{}, &Guard{},
	} {
		if m.Signature() == "" || seen[m.Signature()] {
			t.Errorf("signature %q", m.Signature())
		}
		seen[m.Signature()] = true
	}
}

func TestRecoverAnswersInTheClientFormatAndLogsNoRequestData(t *testing.T) {
	log := &tests.Log{}
	r := newRoute(t)
	r.Recover(Recover(log))
	r.Get("/boom", func(contractshttp.Context) contractshttp.Response { panic("kaboom\nforged line") })
	req := httptest.NewRequest(http.MethodGet, "/boom?key=sk-secret", nil)
	req.Header.Set("Authorization", "Bearer sk-secret-token")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != 500 || !strings.Contains(rec.Body.String(), `"error"`) || strings.Contains(rec.Body.String(), "kaboom") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("content type %q", rec.Header().Get("Content-Type"))
	}
	joined := strings.Join(log.Lines(), "\n")
	if !strings.Contains(joined, "kaboom") {
		t.Fatalf("the panic was not logged: %q", joined)
	}
	for _, leak := range []string{"sk-secret", "Authorization", "key="} {
		if strings.Contains(joined, leak) {
			t.Errorf("%q leaked into the log: %q", leak, joined)
		}
	}
	if strings.Contains(strings.SplitN(joined, "\n", 2)[0], "forged line") && strings.Count(joined, "\nforged line") > 0 {
		t.Errorf("a panic value forged a log line: %q", joined)
	}
}

func TestRecoverUsesTheEndpointFormat(t *testing.T) {
	r := newRoute(t)
	r.Recover(Recover(&tests.Log{}))
	r.Get("/v1/messages/boom", func(contractshttp.Context) contractshttp.Response { panic("x") })
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/messages/boom", nil))
	if rec.Code != 500 || !strings.Contains(rec.Body.String(), `"type":"error"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestRecoverAbortsTheConnectionWhenTheResponseAlreadyStarted(t *testing.T) {
	r := newRoute(t)
	r.Recover(Recover(&tests.Log{}))
	r.Get("/half", func(ctx contractshttp.Context) contractshttp.Response {
		_, _ = ctx.Response().Writer().Write([]byte("partial"))
		panic("after the first byte")
	})
	var got any
	func() {
		defer func() { got = recover() }()
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/half", nil))
	}()
	if got != http.ErrAbortHandler {
		t.Fatalf("recovered %v, want http.ErrAbortHandler", got)
	}
}

func TestRecoverKeepsAnAbortHandlerPanicAsIs(t *testing.T) {
	r := newRoute(t)
	r.Recover(Recover(&tests.Log{}))
	r.Get("/abort", func(contractshttp.Context) contractshttp.Response { panic(http.ErrAbortHandler) })
	var got any
	func() {
		defer func() { got = recover() }()
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/abort", nil))
	}()
	if got != http.ErrAbortHandler {
		t.Fatalf("recovered %v", got)
	}
}
