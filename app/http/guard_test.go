package http

import (
	"context"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	contractshttp "github.com/goravel/framework/contracts/http"
	goravelgin "github.com/goravel/gin"

	"rllmx/app/access/auth"
	"rllmx/app/access/identity"
	"rllmx/tests"
)

type fakeAuth struct {
	session    bool
	role       string
	uid        string
	key        bool
	invalid    bool
	required   bool
	requireKey bool
}

func (f fakeAuth) Session(*nethttp.Request) bool { return f.session }
func (f fakeAuth) SessionInfo(*nethttp.Request) (auth.Session, bool) {
	if !f.session {
		return auth.Session{}, false
	}
	role := f.role
	if role == "" {
		role = auth.RoleAdmin
	}
	return auth.Session{Role: role, UserID: f.uid}, true
}
func (f fakeAuth) RequireLogin(context.Context) bool  { return f.required }
func (f fakeAuth) RequireAPIKey(context.Context) bool { return f.requireKey }
func (f fakeAuth) APIKey(*nethttp.Request) identity.KeyResult {
	switch {
	case f.key:
		return identity.KeyResult{Status: identity.KeyValid, Caller: identity.Caller{ID: "key-1", Name: "ci"}}
	case f.invalid:
		return identity.KeyResult{Status: identity.KeyInvalid}
	}
	return identity.KeyResult{}
}

// guardMiddleware is the adapter WithMiddleware installs.
type guardMiddleware struct{ g *Guard }

func (guardMiddleware) Signature() string                  { return "test:guard" }
func (m guardMiddleware) Handle(ctx contractshttp.Context) { m.g.Protect(ctx) }

type app struct {
	guard *Guard
	route *goravelgin.Route
	h     nethttp.Handler
}

func reached(ctx contractshttp.Context) contractshttp.Response {
	return ctx.Response().String(200, "reached")
}

func newApp(t *testing.T, a Authenticator, trust bool) *app {
	t.Helper()
	r := tests.NewRoute(t)
	g := NewGuard(a, trust)
	r.SetGlobalMiddleware([]contractshttp.Middleware{guardMiddleware{g}})
	r.Fallback(g.Fallback(r.GetRoutes))
	return &app{guard: g, route: r, h: Rewrite(r)}
}

func guardApp(t *testing.T, a Authenticator, trust bool) *app {
	t.Helper()
	s := newApp(t, a, trust)
	s.guard.Handle(s.route, ClassPublic, "GET", "/api/pub", reached)
	s.guard.Handle(s.route, ClassProtected, "GET", "/api/prot", reached)
	s.guard.Handle(s.route, ClassProtected, "POST", "/api/prot", reached)
	s.guard.Handle(s.route, ClassAlwaysProtected, "GET", "/api/always", reached)
	s.guard.Handle(s.route, ClassLocalOnly, "GET", "/api/local", reached)
	s.guard.Handle(s.route, ClassLLM, "POST", "/v1/chat/completions", reached)
	s.guard.Handle(s.route, ClassLLM, "POST", "/v1/messages", reached)
	s.guard.Handle(s.route, ClassLLM, "POST", "/v1beta/models/*path", reached)
	s.guard.Handle(s.route, ClassPublic, "GET", "/", reached)
	s.guard.Handle(s.route, ClassPage, "GET", "/dashboard/home", reached)
	return s
}

type peer struct {
	name   string
	remote string
	host   string
}

var (
	localPeer  = peer{"local", "127.0.0.1:5000", "localhost:20128"}
	remotePeer = peer{"remote", "203.0.113.9:5000", "gw.example.com"}
)

func (s *app) call(p peer, method, path string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr, req.Host = p.remote, p.host
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.h.ServeHTTP(rec, req)
	return rec
}

func TestGuardMatrixForDashboardClasses(t *testing.T) {
	sess := func(required bool) fakeAuth { return fakeAuth{session: true, required: required} }
	anon := func(required bool) fakeAuth { return fakeAuth{required: required} }
	cases := []struct {
		path string
		peer peer
		auth fakeAuth
		want int
		note string
	}{
		{"/api/pub", remotePeer, anon(true), 200, "public"},
		{"/api/pub", localPeer, anon(true), 200, "public"},

		{"/api/prot", remotePeer, anon(true), 401, "protected, no session"},
		{"/api/prot", localPeer, anon(true), 401, "protected, no session, local"},
		{"/api/prot", remotePeer, sess(true), 200, "protected, session"},
		{"/api/prot", localPeer, sess(true), 200, "protected, session, local"},
		{"/api/prot", localPeer, anon(false), 200, "login off, local peer"},
		{"/api/prot", remotePeer, anon(false), 401, "login off, remote peer still needs a session"},
		{"/api/prot", remotePeer, sess(false), 200, "login off, remote with session"},

		{"/api/always", localPeer, anon(false), 401, "always: login off does not open it (local)"},
		{"/api/always", remotePeer, anon(false), 401, "always: login off does not open it (remote)"},
		{"/api/always", localPeer, anon(true), 401, "always: no session"},
		{"/api/always", remotePeer, sess(true), 200, "always: session, remote ok"},
		{"/api/always", localPeer, sess(false), 200, "always: session, login off"},

		{"/api/local", localPeer, sess(true), 200, "local-only: local + session"},
		{"/api/local", localPeer, sess(false), 200, "local-only: local + session, login off"},
		{"/api/local", localPeer, anon(true), 401, "local-only: local, no session"},
		{"/api/local", localPeer, anon(false), 401, "local-only: login off must not open it"},
		{"/api/local", remotePeer, sess(true), 403, "local-only: remote with session"},
		{"/api/local", remotePeer, anon(false), 403, "local-only: remote, login off"},
		{"/api/local", remotePeer, anon(true), 403, "local-only: remote, no session"},
	}
	for _, c := range cases {
		rec := guardApp(t, c.auth, true).call(c.peer, "GET", c.path, nil)
		if rec.Code != c.want {
			t.Errorf("%s [%s %s]: got %d (%s), want %d", c.note, c.path, c.peer.name, rec.Code, strings.TrimSpace(rec.Body.String()), c.want)
		}
		if c.want != 200 && strings.Contains(rec.Body.String(), "reached") {
			t.Errorf("%s: the handler ran", c.note)
		}
	}
}

func TestGuardMatrixForTheClientAPI(t *testing.T) {
	cases := []struct {
		peer peer
		auth fakeAuth
		hdr  map[string]string
		want int
		note string
	}{
		{remotePeer, fakeAuth{key: true}, nil, 200, "remote with a valid key"},
		{remotePeer, fakeAuth{}, nil, 401, "remote without a key"},
		{remotePeer, fakeAuth{session: true}, nil, 401, "a dashboard session is not an API key"},
		{localPeer, fakeAuth{}, nil, 200, "local without a key while the key is not required"},
		{localPeer, fakeAuth{key: true}, nil, 200, "local with a key"},
		{localPeer, fakeAuth{requireKey: true}, nil, 401, "local without a key while the key is required"},
		{localPeer, fakeAuth{requireKey: true, invalid: true}, nil, 401, "local with a bad key while the key is required"},
		{localPeer, fakeAuth{requireKey: true, key: true}, nil, 200, "local with a key while the key is required"},
		{localPeer, fakeAuth{invalid: true}, nil, 200, "local with a bad key while the key is not required"},
		{remotePeer, fakeAuth{invalid: true}, nil, 401, "remote with a bad key"},
		{remotePeer, fakeAuth{}, nil, 401, "remote without a key, key not required"},
		{localPeer, fakeAuth{}, map[string]string{"X-Forwarded-For": "203.0.113.9"}, 401, "local peer behind a proxy header"},
		{localPeer, fakeAuth{}, map[string]string{"Origin": "https://evil.com"}, 401, "browser page from another origin"},
		{localPeer, fakeAuth{}, map[string]string{"Origin": "http://localhost:3000"}, 200, "browser page on another localhost port"},
		{localPeer, fakeAuth{}, map[string]string{"Sec-Fetch-Site": "cross-site"}, 401, "cross-site fetch"},
		{peer{"rebind", "127.0.0.1:5000", "attacker.example"}, fakeAuth{}, nil, 401, "DNS rebinding: loopback peer, foreign Host"},
	}
	for _, c := range cases {
		for _, path := range []string{"/v1/chat/completions", "/v1/messages", "/v1beta/models/gemini:generateContent"} {
			if rec := guardApp(t, c.auth, true).call(c.peer, "POST", path, c.hdr); rec.Code != c.want {
				t.Errorf("%s [%s]: got %d, want %d", c.note, path, rec.Code, c.want)
			}
		}
	}
}

// throttledAuth adds the key limiter to fakeAuth: a lock after limit refused keys
// from one peer.
type throttledAuth struct {
	fakeAuth
	limit int
	fails map[string]int
}

func (a *throttledAuth) KeyLocked(r *nethttp.Request) time.Duration {
	if a.fails[r.RemoteAddr] >= a.limit {
		return 30 * time.Second
	}
	return 0
}

func (a *throttledAuth) KeyFailed(r *nethttp.Request) { a.fails[r.RemoteAddr]++ }

func TestARemoteCallerGuessingKeysIsLocked(t *testing.T) {
	a := &throttledAuth{fakeAuth: fakeAuth{invalid: true}, limit: 3, fails: map[string]int{}}
	s := guardApp(t, a, true)
	key := map[string]string{"Authorization": "Bearer sk-guess"}
	for i := range 3 {
		if rec := s.call(remotePeer, "POST", "/v1/chat/completions", key); rec.Code != 401 {
			t.Fatalf("guess %d: got %d, want 401", i, rec.Code)
		}
	}
	rec := s.call(remotePeer, "POST", "/v1/chat/completions", key)
	if rec.Code != 429 || rec.Header().Get("Retry-After") != "30" {
		t.Fatalf("locked caller got %d with Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	if a.fails[remotePeer.remote] != 3 {
		t.Fatalf("a locked request counted as a failure: %d", a.fails[remotePeer.remote])
	}
}

func TestALockedCallerGetsNoAnswerForAValidKey(t *testing.T) {
	a := &throttledAuth{fakeAuth: fakeAuth{key: true}, limit: 1, fails: map[string]int{remotePeer.remote: 1}}
	rec := guardApp(t, a, true).call(remotePeer, "POST", "/v1/chat/completions", map[string]string{"Authorization": "Bearer sk-real"})
	if rec.Code != 429 {
		t.Fatalf("got %d: a locked address must not learn whether a key is right", rec.Code)
	}
}

func TestALockedCallerWithoutAKeyStillGets401(t *testing.T) {
	a := &throttledAuth{fakeAuth: fakeAuth{}, limit: 1, fails: map[string]int{remotePeer.remote: 9}}
	if rec := guardApp(t, a, true).call(remotePeer, "POST", "/v1/chat/completions", nil); rec.Code != 401 {
		t.Fatalf("got %d: a call with no key is not a guess", rec.Code)
	}
}

func TestAnotherCallerIsNotLockedByTheFirst(t *testing.T) {
	a := &throttledAuth{fakeAuth: fakeAuth{key: true}, limit: 1, fails: map[string]int{"198.51.100.1:5000": 9}}
	rec := guardApp(t, a, true).call(remotePeer, "POST", "/v1/chat/completions", map[string]string{"Authorization": "Bearer sk-real"})
	if rec.Code != 200 {
		t.Fatalf("got %d, want 200", rec.Code)
	}
}

func TestALocalCallerAndAKeylessCallIsNeverCounted(t *testing.T) {
	a := &throttledAuth{fakeAuth: fakeAuth{invalid: true}, limit: 1, fails: map[string]int{}}
	s := guardApp(t, a, true)
	for range 3 {
		s.call(localPeer, "POST", "/v1/chat/completions", map[string]string{"Authorization": "Bearer sk-bad"})
	}
	if len(a.fails) != 0 {
		t.Fatalf("a local caller was counted: %v", a.fails)
	}
	b := &throttledAuth{fakeAuth: fakeAuth{}, limit: 1, fails: map[string]int{}}
	for range 3 {
		if rec := guardApp(t, b, true).call(remotePeer, "POST", "/v1/chat/completions", nil); rec.Code != 401 {
			t.Fatalf("keyless remote call got %d, want 401", rec.Code)
		}
	}
	if len(b.fails) != 0 {
		t.Fatalf("a call without a key was counted: %v", b.fails)
	}
}

func TestClientAPIDenialIsInTheClientFormat(t *testing.T) {
	s := guardApp(t, fakeAuth{}, true)
	for path, want := range map[string]string{
		"/v1/chat/completions":                  `"code":"invalid_api_key"`,
		"/v1/messages":                          `"type":"authentication_error"`,
		"/v1beta/models/gemini:generateContent": `"status":"UNAUTHENTICATED"`,
	} {
		rec := s.call(remotePeer, "POST", path, nil)
		if rec.Code != 401 || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
}

func TestTrustLoopbackOffMakesEveryRequestRemote(t *testing.T) {
	s := guardApp(t, fakeAuth{session: true, required: false}, false)
	if rec := s.call(localPeer, "GET", "/api/local", nil); rec.Code != 403 {
		t.Errorf("local-only with trust off: %d", rec.Code)
	}
	if rec := s.call(localPeer, "POST", "/v1/chat/completions", nil); rec.Code != 401 {
		t.Errorf("client API with trust off and no key: %d", rec.Code)
	}
	s = guardApp(t, fakeAuth{required: false}, false)
	if rec := s.call(localPeer, "GET", "/api/prot", nil); rec.Code != 401 {
		t.Errorf("anonymous access with login off must not apply when loopback is not trusted: %d", rec.Code)
	}
}

func TestForgedForwardedHeaderFromARemotePeerChangesNothing(t *testing.T) {
	s := guardApp(t, fakeAuth{required: false}, true)
	forged := peer{"forged", "203.0.113.9:5000", "localhost:20128"}
	for _, h := range []map[string]string{
		{"X-Forwarded-For": "127.0.0.1"}, {"X-Real-Ip": "127.0.0.1"}, {"Forwarded": "for=127.0.0.1"}, {"X-Forwarded-Host": "localhost"},
	} {
		if rec := s.call(forged, "GET", "/api/prot", h); rec.Code != 401 {
			t.Errorf("%v: remote peer with a forged header got %d", h, rec.Code)
		}
		if rec := s.call(forged, "POST", "/v1/chat/completions", h); rec.Code != 401 {
			t.Errorf("%v: client API %d", h, rec.Code)
		}
	}
}

func TestUnknownRoutes(t *testing.T) {
	s := guardApp(t, fakeAuth{required: true}, true)
	if rec := s.call(remotePeer, "GET", "/api/nope", nil); rec.Code != 401 {
		t.Errorf("unknown /api path, anonymous: %d (must not reveal which paths exist)", rec.Code)
	}
	if rec := s.call(remotePeer, "POST", "/v1/unknown", nil); rec.Code != 401 {
		t.Errorf("unknown /v1 path, anonymous: %d", rec.Code)
	}
	s = guardApp(t, fakeAuth{session: true, required: true}, true)
	if rec := s.call(remotePeer, "GET", "/api/nope", nil); rec.Code != 404 {
		t.Errorf("unknown /api path, authenticated: %d", rec.Code)
	}
	s = guardApp(t, fakeAuth{key: true}, true)
	rec := s.call(remotePeer, "POST", "/v1/unknown", nil)
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), `"code":"model_not_found"`) {
		t.Errorf("unknown /v1 path with a key: %d %s", rec.Code, rec.Body.String())
	}
	rec = s.call(remotePeer, "POST", "/v1/messages/nope", nil)
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), `"type":"error"`) {
		t.Errorf("unknown Anthropic path should answer in that format: %d %s", rec.Code, rec.Body.String())
	}
}

func TestWrongMethodNeverRevealsAllowedMethodsToAnAnonymousCaller(t *testing.T) {
	s := guardApp(t, fakeAuth{required: true}, true)
	rec := s.call(remotePeer, "DELETE", "/api/prot", nil)
	if rec.Code != 401 {
		t.Fatalf("anonymous wrong method: %d", rec.Code)
	}
	if rec.Header().Get("Allow") != "" {
		t.Fatalf("Allow header leaked: %q", rec.Header().Get("Allow"))
	}
}

func TestWrongMethodAnswers405WithAllowForACallerWhoIsLetIn(t *testing.T) {
	s := guardApp(t, fakeAuth{session: true, required: true}, true)
	rec := s.call(remotePeer, "DELETE", "/api/prot", nil)
	if rec.Code != 405 {
		t.Fatalf("authenticated wrong method: %d", rec.Code)
	}
	allow := rec.Header().Get("Allow")
	for _, m := range []string{"GET", "HEAD", "POST"} {
		if !strings.Contains(allow, m) {
			t.Errorf("Allow = %q, missing %s", allow, m)
		}
	}
	if strings.Contains(allow, "DELETE") {
		t.Errorf("Allow = %q lists the method that was refused", allow)
	}

	s = guardApp(t, fakeAuth{key: true}, true)
	rec = s.call(remotePeer, "GET", "/v1/messages", nil)
	if rec.Code != 405 || !strings.Contains(rec.Body.String(), `"type":"invalid_request_error"`) || rec.Header().Get("Allow") != "POST" {
		t.Errorf("client API wrong method: %d allow=%q %s", rec.Code, rec.Header().Get("Allow"), rec.Body.String())
	}
}

func TestWrongMethodOnAWildcardRoute(t *testing.T) {
	s := guardApp(t, fakeAuth{key: true}, true)
	rec := s.call(remotePeer, "GET", "/v1beta/models/gemini:generateContent", nil)
	if rec.Code != 405 || rec.Header().Get("Allow") != "POST" {
		t.Fatalf("%d allow=%q", rec.Code, rec.Header().Get("Allow"))
	}
}

func TestOptionsWithoutPreflightGoesThroughTheGuard(t *testing.T) {
	s := guardApp(t, fakeAuth{}, true)
	if rec := s.call(remotePeer, "OPTIONS", "/v1/chat/completions", nil); rec.Code != 401 {
		t.Fatalf("a bare OPTIONS on the client API got %d", rec.Code)
	}
	if rec := s.call(remotePeer, "OPTIONS", "/api/prot", nil); rec.Code != 401 {
		t.Fatalf("OPTIONS on /api got %d", rec.Code)
	}
}

func TestDashboardPagesRedirectToLoginWithoutASession(t *testing.T) {
	s := guardApp(t, fakeAuth{required: true}, true)
	rec := s.call(remotePeer, "GET", "/dashboard/home", nil)
	if rec.Code != nethttp.StatusFound || rec.Header().Get("Location") != "/login" {
		t.Fatalf("%d %q", rec.Code, rec.Header().Get("Location"))
	}
	if strings.Contains(rec.Body.String(), "reached") {
		t.Fatal("the page ran")
	}
	s = guardApp(t, fakeAuth{session: true, required: true}, true)
	if rec := s.call(remotePeer, "GET", "/dashboard/home", nil); rec.Code != 200 {
		t.Fatalf("with a session: %d", rec.Code)
	}
	s = guardApp(t, fakeAuth{required: false}, true)
	if rec := s.call(localPeer, "GET", "/dashboard/home", nil); rec.Code != 200 {
		t.Fatalf("login off, local: %d", rec.Code)
	}
	if rec := s.call(remotePeer, "GET", "/dashboard/home", nil); rec.Code != nethttp.StatusFound {
		t.Fatalf("login off, remote: %d", rec.Code)
	}
	s = guardApp(t, fakeAuth{required: true}, true)
	if rec := s.call(remotePeer, "GET", "/dashboard/missing", nil); rec.Code != nethttp.StatusFound {
		t.Fatalf("an unrouted dashboard page must still redirect an anonymous caller: %d", rec.Code)
	}
}

func TestDefaultAuthenticatorFailsClosed(t *testing.T) {
	s := newApp(t, nil, true)
	s.guard.Handle(s.route, ClassProtected, "GET", "/api/prot", reached)
	s.guard.Handle(s.route, ClassLLM, "POST", "/v1/x", reached)
	s.guard.Handle(s.route, ClassLocalOnly, "GET", "/api/local", reached)
	for _, p := range []peer{localPeer, remotePeer} {
		if rec := s.call(p, "GET", "/api/prot", nil); rec.Code != 401 {
			t.Errorf("%s: protected route open without an authenticator: %d", p.name, rec.Code)
		}
		if rec := s.call(p, "GET", "/api/local", nil); rec.Code == 200 {
			t.Errorf("%s: local-only route open without an authenticator", p.name)
		}
		if rec := s.call(p, "POST", "/v1/x", nil); rec.Code != 401 {
			t.Errorf("%s: client API open without an authenticator: %d", p.name, rec.Code)
		}
	}
}

func TestTheCallerReachesTheHandler(t *testing.T) {
	var got identity.Caller
	var ok bool
	s := newApp(t, fakeAuth{key: true}, true)
	s.guard.Handle(s.route, ClassLLM, "POST", "/v1/who", func(ctx contractshttp.Context) contractshttp.Response {
		got, ok = CallerFrom(ctx)
		return ctx.Response().String(200, "ok")
	})
	if rec := s.call(remotePeer, "POST", "/v1/who", nil); rec.Code != 200 || !ok || got.ID != "key-1" {
		t.Fatalf("status %d caller %+v ok=%v", rec.Code, got, ok)
	}

	got, ok = identity.Caller{}, false
	s = newApp(t, fakeAuth{}, true)
	s.guard.Handle(s.route, ClassLLM, "POST", "/v1/who", func(ctx contractshttp.Context) contractshttp.Response {
		got, ok = CallerFrom(ctx)
		return ctx.Response().String(200, "ok")
	})
	if rec := s.call(localPeer, "POST", "/v1/who", nil); rec.Code != 200 || !ok || !got.Anonymous || got.ID != identity.AnonymousLocal().ID {
		t.Fatalf("local without a key: status %d caller %+v ok=%v", rec.Code, got, ok)
	}

	ok = false
	s = newApp(t, fakeAuth{session: true, required: true}, true)
	s.guard.Handle(s.route, ClassProtected, "GET", "/api/who", func(ctx contractshttp.Context) contractshttp.Response {
		_, ok = CallerFrom(ctx)
		return ctx.Response().String(200, "ok")
	})
	if s.call(remotePeer, "GET", "/api/who", nil); ok {
		t.Fatal("a dashboard route received a client API caller")
	}
}

// The driver keeps its route table in a package-level map shared by every Route
// in the process, so routes added by other tests also show up in GetRoutes. The
// checks below therefore look for specific entries instead of an empty list.
func TestARouteOnTheBareFacadeIsReported(t *testing.T) {
	s := guardApp(t, fakeAuth{}, true)
	s.route.Get("/api/sneaky", reached)
	missing := strings.Join(s.guard.Unclassified(s.route.GetRoutes()), "|")
	for _, want := range []string{"GET /api/sneaky", "HEAD /api/sneaky"} {
		if !strings.Contains(missing, want) {
			t.Errorf("%q was not reported: %s", want, missing)
		}
	}
	for _, own := range []string{"/api/pub", "/api/prot", "/api/always", "/api/local", "/v1/chat/completions", "/v1/messages", "/dashboard/home"} {
		if strings.Contains(missing, " "+own) {
			t.Errorf("a route declared through Handle was reported: %s", own)
		}
	}
}

func TestARouteOutsideTheGuardTableIsReported(t *testing.T) {
	s := guardApp(t, fakeAuth{}, true)
	infos := []contractshttp.Info{
		{Method: "GET|HEAD", Path: "/api/pub"},
		{Method: "GET|HEAD", Path: "/api/sneaky"},
		{Method: "POST", Path: "/api/prot"},
		{Method: "POST", Path: "/api/other"},
		{Method: "ANY", Path: "/v1/any"},
		{Method: "STATIC", Path: "/assets"},
	}
	got := s.guard.Unclassified(infos)
	want := []string{"ANY /v1/any", "GET /api/sneaky", "HEAD /api/sneaky", "POST /api/other", "STATIC /assets"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestHandleRejectsABadClassAndDuplicateRegistrations(t *testing.T) {
	s := newApp(t, nil, true)
	mustPanic := func(name string, fn func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Errorf("%s did not panic", name)
			}
		}()
		fn()
	}
	mustPanic("unknown class", func() { s.guard.Handle(s.route, Class(99), "GET", "/api/x", reached) })
	mustPanic("zero class", func() { s.guard.Handle(s.route, Class(0), "GET", "/api/x", reached) })
	s.guard.Handle(s.route, ClassProtected, "GET", "/api/dup", reached)
	mustPanicWith := func(name, msg string, fn func()) {
		t.Helper()
		defer func() {
			r := recover()
			if s, _ := r.(string); !strings.Contains(s, msg) {
				t.Errorf("%s: panic = %v, want a message containing %q", name, r, msg)
			}
		}()
		fn()
	}
	mustPanicWith("same route twice", "registered twice", func() { s.guard.Handle(s.route, ClassProtected, "GET", "/api/dup", reached) })
	mustPanicWith("same route in another class", "registered twice", func() { s.guard.Handle(s.route, ClassPublic, "GET", "/api/dup", reached) })
	s.guard.Handle(s.route, ClassProtected, "POST", "/api/dup", reached)
	mustPanic("client route in a dashboard class", func() { s.guard.Handle(s.route, ClassProtected, "POST", "/v1/x", reached) })
	mustPanic("client class on a dashboard path", func() { s.guard.Handle(s.route, ClassLLM, "POST", "/api/x", reached) })
	mustPanic("page under /api", func() { s.guard.Handle(s.route, ClassPage, "GET", "/api/page", reached) })
	mustPanic("unsupported method", func() { s.guard.Handle(s.route, ClassProtected, "TRACE", "/api/trace", reached) })
}

func TestARejectedRegistrationLeavesNoTraceInTheTable(t *testing.T) {
	s := newApp(t, nil, true)
	func() {
		defer func() { _ = recover() }()
		s.guard.Handle(s.route, ClassLLM, "POST", "/api/x", reached)
	}()
	if _, ok := s.guard.RouteClass("POST", "/api/x"); ok {
		t.Fatal("a rejected registration was recorded")
	}
}

func TestRouteClassReportsTheClassARouteWasRegisteredWith(t *testing.T) {
	s := guardApp(t, fakeAuth{}, true)
	for key, want := range map[[2]string]Class{
		{"GET", "/api/pub"}:              ClassPublic,
		{"HEAD", "/api/pub"}:             ClassPublic,
		{"GET", "/api/prot"}:             ClassProtected,
		{"POST", "/api/prot"}:            ClassProtected,
		{"GET", "/api/local"}:            ClassLocalOnly,
		{"POST", "/v1/messages"}:         ClassLLM,
		{"GET", "/dashboard/home"}:       ClassPage,
		{"POST", "/v1beta/models/*path"}: ClassLLM,
	} {
		if got, ok := s.guard.RouteClass(key[0], key[1]); !ok || got != want {
			t.Errorf("%s %s: got %d ok=%v, want %d", key[0], key[1], got, ok, want)
		}
	}
	if _, ok := s.guard.RouteClass("DELETE", "/api/prot"); ok {
		t.Error("a method that was never registered has a class")
	}
}

func TestPathParametersAreClassifiedByTheirPattern(t *testing.T) {
	s := newApp(t, fakeAuth{session: true, required: true}, true)
	s.guard.Handle(s.route, ClassAlwaysProtected, "GET", "/api/providers/{id}", reached)
	if rec := s.call(remotePeer, "GET", "/api/providers/abc", nil); rec.Code != 200 {
		t.Fatalf("a parameter route was not found by its class: %d", rec.Code)
	}
	s = newApp(t, fakeAuth{required: false}, true)
	s.guard.Handle(s.route, ClassAlwaysProtected, "GET", "/api/providers/{id}", reached)
	if rec := s.call(localPeer, "GET", "/api/providers/abc", nil); rec.Code != 401 {
		t.Fatalf("login off must not open an always-protected parameter route: %d", rec.Code)
	}
}

func TestMatchPath(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"/api/prot", "/api/prot", true},
		{"/api/prot", "/api/other", false},
		{"/api/prot", "/api/prot/x", false},
		{"/api/prot/x", "/api/prot", false},
		{"/api/p/{id}", "/api/p/abc", true},
		{"/api/p/{id}", "/api/p/", false},
		{"/api/p/{id}", "/api/p", false},
		{"/api/p/{id}/models", "/api/p/abc/models", true},
		{"/v1beta/models/*path", "/v1beta/models/gemini:generateContent", true},
		{"/v1beta/models/*path", "/v1beta/models", false},
		{"/", "/", true},
		{"/", "/x", false},
	}
	for _, c := range cases {
		if got := matchPath(c.pattern, c.path); got != c.want {
			t.Errorf("matchPath(%q, %q) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}
