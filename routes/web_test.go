package routes

import (
	"context"
	"errors"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	contractshttp "github.com/goravel/framework/contracts/http"
	goravelgin "github.com/goravel/gin"

	"rllmx/app/access/identity"
	apphttp "rllmx/app/http"
	"rllmx/app/http/controllers"
	"rllmx/app/http/middleware"
	"rllmx/tests"
)

// signedIn is an authenticator for a dashboard session, so a request gets past
// the guard and reaches whatever answers an unmatched path.
type signedIn struct{}

func (signedIn) Session(*nethttp.Request) bool              { return true }
func (signedIn) APIKey(*nethttp.Request) identity.KeyResult { return identity.KeyResult{} }
func (signedIn) RequireLogin(context.Context) bool          { return true }
func (signedIn) RequireAPIKey(context.Context) bool         { return true }

func build(t *testing.T) (*goravelgin.Route, *apphttp.Guard) {
	t.Helper()
	return buildWith(t, nil)
}

func buildWith(t *testing.T, auth apphttp.Authenticator) (*goravelgin.Route, *apphttp.Guard) {
	t.Helper()
	r := tests.NewRoute(t)
	g := apphttp.NewGuard(auth, true)
	r.SetGlobalMiddleware([]contractshttp.Middleware{&middleware.RequestID{}, &middleware.Guard{Guard: g}})
	Web(r, g, Base{Version: "1.2.3", Commit: "abc123", Connections: func(context.Context) (int64, error) { return 0, nil }})
	return r, g
}

func call(r nethttp.Handler, method, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = "203.0.113.9:5000"
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestHealthIsPublicThroughTheGuard(t *testing.T) {
	r, _ := build(t)
	for _, m := range []string{nethttp.MethodGet, nethttp.MethodHead} {
		rec := call(r, m, "/api/health")
		if rec.Code != 200 {
			t.Errorf("%s: %d", m, rec.Code)
		}
		if m == nethttp.MethodGet && strings.TrimSpace(rec.Body.String()) != `{"ok":true}` {
			t.Errorf("body %q", rec.Body.String())
		}
	}
}

func TestEveryRouteWebDeclaresIsInTheGuardTable(t *testing.T) {
	r, g := build(t)
	for _, info := range r.GetRoutes() {
		if !strings.HasPrefix(info.Path, "/api/") {
			continue
		}
		if missing := g.Unclassified([]contractshttp.Info{info}); len(missing) != 0 {
			t.Errorf("routes outside the guard table: %v", missing)
		}
	}
	for _, path := range []string{"/api/health", "/api/version", "/api/init"} {
		if class, ok := g.RouteClass("GET", path); !ok || class != apphttp.ClassPublic {
			t.Errorf("%s class = %d ok=%v", path, class, ok)
		}
	}
}

func TestVersionReportsTheBuildAndNeverAPackageRegistry(t *testing.T) {
	r, _ := build(t)
	rec := call(r, nethttp.MethodGet, "/api/version")
	if rec.Code != 200 {
		t.Fatalf("%d", rec.Code)
	}
	body := strings.TrimSpace(rec.Body.String())
	for _, want := range []string{`"currentVersion":"1.2.3"`, `"commit":"abc123"`, `"latestVersion":null`, `"hasUpdate":false`} {
		if !strings.Contains(body, want) {
			t.Errorf("%s is missing from %s", want, body)
		}
	}
}

func TestInitReportsSetupNeededFromTheConnectionCount(t *testing.T) {
	for _, tc := range []struct {
		n     int64
		err   error
		setup bool
	}{
		{n: 0, setup: true},
		{n: 3, setup: false},
		{n: 0, err: errors.New("store closed"), setup: false},
	} {
		called := false
		r := tests.NewRoute(t)
		g := apphttp.NewGuard(nil, true)
		r.SetGlobalMiddleware(nil)
		Web(r, g, Base{Connections: func(context.Context) (int64, error) {
			called = true
			return tc.n, tc.err
		}})
		rec := call(r, nethttp.MethodGet, "/api/init")
		if !called || rec.Code != 200 {
			t.Fatalf("%d called=%v", rec.Code, called)
		}
		want := `"needsSetup":false`
		if tc.setup {
			want = `"needsSetup":true`
		}
		if body := strings.TrimSpace(rec.Body.String()); !strings.Contains(body, want) || !strings.Contains(body, `"initialized":true`) {
			t.Errorf("body %q, want %s", body, want)
		}
	}
}

func TestInitWithoutACounterNeedsNoSetup(t *testing.T) {
	r := tests.NewRoute(t)
	g := apphttp.NewGuard(nil, true)
	r.SetGlobalMiddleware(nil)
	Web(r, g, Base{})
	rec := call(r, nethttp.MethodGet, "/api/init")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"needsSetup":false`) {
		t.Fatalf("%d %q", rec.Code, rec.Body.String())
	}
}

func TestAnUnknownPathIsAnsweredByTheFallback(t *testing.T) {
	r, _ := build(t)
	if rec := call(r, nethttp.MethodGet, "/api/nope"); rec.Code != 401 {
		t.Fatalf("unknown /api path, anonymous: %d", rec.Code)
	}
	if rec := call(r, nethttp.MethodPost, "/v1/chat/completions"); rec.Code != 401 {
		t.Fatalf("unknown /v1 path, anonymous: %d", rec.Code)
	}
}

func TestFallbackAnswersACallerWhoIsLetIn(t *testing.T) {
	r, _ := buildWith(t, signedIn{})
	rec := call(r, nethttp.MethodGet, "/api/nope")
	if rec.Code != 404 || strings.TrimSpace(rec.Body.String()) != `{"error":"Not found"}` {
		t.Fatalf("unknown path: %d %q (the framework's plain 404 would mean no fallback is wired)", rec.Code, rec.Body.String())
	}
	rec = call(r, nethttp.MethodDelete, "/api/health")
	if rec.Code != 405 || !strings.Contains(rec.Header().Get("Allow"), "GET") {
		t.Fatalf("wrong method: %d allow=%q", rec.Code, rec.Header().Get("Allow"))
	}
}

func TestThereIsNoShutdownRoute(t *testing.T) {
	r, _ := build(t)
	for _, p := range []string{"/api/shutdown", "/api/update", "/api/restart"} {
		for _, m := range []string{nethttp.MethodGet, nethttp.MethodPost} {
			if rec := call(r, m, p); rec.Code != 401 {
				t.Errorf("%s %s: %d (an anonymous caller must not learn whether the route exists)", m, p, rec.Code)
			}
		}
	}
}

func TestEveryChatRouteIsAnLLMRouteThatNeedsAKey(t *testing.T) {
	r := tests.NewRoute(t)
	g := apphttp.NewGuard(nil, true)
	r.SetGlobalMiddleware([]contractshttp.Middleware{&middleware.RequestID{}, &middleware.Guard{Guard: g}})
	Web(r, g, Base{Chat: &controllers.ChatController{}})
	for _, p := range []string{
		"/v1/chat/completions", "/v1/messages", "/v1/messages/count_tokens", "/v1/responses", "/v1/responses/compact",
		"/v1beta/models/*path", "/v1/api/chat",
	} {
		if class, ok := g.RouteClass("POST", p); !ok || class != apphttp.ClassLLM {
			t.Errorf("%s: class = %d ok=%v", p, class, ok)
		}
		if _, ok := g.RouteClass("GET", p); ok {
			t.Errorf("GET %s is declared", p)
		}
	}
	for _, p := range []string{"/v1/messages", "/v1beta/models/a:generateContent", "/v1/api/chat"} {
		if rec := call(r, nethttp.MethodPost, p); rec.Code != 401 {
			t.Errorf("%s without a key: %d", p, rec.Code)
		}
	}
}

func TestChatCompletionsIsLeftOutWithoutAController(t *testing.T) {
	_, g := build(t)
	if _, ok := g.RouteClass("POST", "/v1/chat/completions"); ok {
		t.Fatal("the route is declared without a controller")
	}
}

func TestModelRoutesAreAnLLMGetAndSitBesideTheChatRoutes(t *testing.T) {
	r := tests.NewRoute(t)
	g := apphttp.NewGuard(nil, true)
	r.SetGlobalMiddleware([]contractshttp.Middleware{&middleware.RequestID{}, &middleware.Guard{Guard: g}})
	Web(r, g, Base{Chat: &controllers.ChatController{}, Models: &controllers.ModelsController{}})
	for _, p := range []string{"/v1/models", "/v1/models/*path", "/v1beta/models"} {
		if class, ok := g.RouteClass("GET", p); !ok || class != apphttp.ClassLLM {
			t.Errorf("GET %s: class = %d ok=%v", p, class, ok)
		}
		if _, ok := g.RouteClass("POST", p); ok && p != "/v1beta/models/*path" {
			t.Errorf("POST %s is declared", p)
		}
	}
	for _, p := range []string{"/v1/models", "/v1/models/info?id=a/b", "/v1/models/openai/gpt-x", "/v1beta/models"} {
		if rec := call(r, nethttp.MethodGet, p); rec.Code != 401 {
			t.Errorf("GET %s without a key: %d", p, rec.Code)
		}
	}
	if rec := call(r, nethttp.MethodPost, "/v1beta/models/a:generateContent"); rec.Code != 401 {
		t.Errorf("the Gemini chat route moved: %d", rec.Code)
	}
}

func TestModelRoutesAreLeftOutWithoutAController(t *testing.T) {
	_, g := build(t)
	if _, ok := g.RouteClass("GET", "/v1/models"); ok {
		t.Fatal("the route is declared without a controller")
	}
}

func oauthRoutes(t *testing.T) (*goravelgin.Route, *apphttp.Guard) {
	t.Helper()
	r := tests.NewRoute(t)
	g := apphttp.NewGuard(nil, true)
	r.SetGlobalMiddleware([]contractshttp.Middleware{&middleware.RequestID{}, &middleware.Guard{Guard: g}})
	Web(r, g, Base{OAuth: &controllers.OAuthController{}, Callback: &controllers.CallbackController{}})
	return r, g
}

func TestTheLoginRoutesNeedASessionAndTheCallbackPageIsPublic(t *testing.T) {
	r, g := oauthRoutes(t)
	for _, rt := range [][2]string{
		{"GET", "/api/oauth/{provider}/authorize"}, {"GET", "/api/oauth/{provider}/device-code"}, {"POST", "/api/oauth/poll"}, {"POST", "/api/oauth/exchange"},
		{"POST", "/api/oauth/callback"}, {"GET", "/api/oauth/status"},
	} {
		if class, ok := g.RouteClass(rt[0], rt[1]); !ok || class != apphttp.ClassAlwaysProtected {
			t.Errorf("%s %s: class = %d ok=%v", rt[0], rt[1], class, ok)
		}
	}
	for _, p := range []string{"/callback", "/static/js/callback.js"} {
		if class, ok := g.RouteClass("GET", p); !ok || class != apphttp.ClassPublic {
			t.Errorf("GET %s: class = %d ok=%v", p, class, ok)
		}
	}
	if rec := call(r, nethttp.MethodGet, "/api/oauth/claude/authorize"); rec.Code != 401 {
		t.Errorf("authorize without a session: %d", rec.Code)
	}
	if rec := call(r, nethttp.MethodPost, "/api/oauth/exchange"); rec.Code != 401 {
		t.Errorf("exchange without a session: %d", rec.Code)
	}
}

func TestTheStaticAndParameterisedLoginRoutesDoNotCollide(t *testing.T) {
	_, g := oauthRoutes(t)
	if _, ok := g.RouteClass("GET", "/api/oauth/status"); !ok {
		t.Fatal("status is not routed beside the {provider} route")
	}
	if _, ok := g.RouteClass("POST", "/api/oauth/exchange"); !ok {
		t.Fatal("exchange is not routed beside the {provider} route")
	}
}

func TestTheLoginRoutesAreLeftOutWithoutTheirControllers(t *testing.T) {
	_, g := build(t)
	for _, p := range []string{"/callback", "/static/js/callback.js", "/api/oauth/status"} {
		if _, ok := g.RouteClass("GET", p); ok {
			t.Errorf("%s is declared without a controller", p)
		}
	}
}
