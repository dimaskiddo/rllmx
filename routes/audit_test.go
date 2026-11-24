package routes

import (
	"context"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	contractshttp "github.com/goravel/framework/contracts/http"

	"rllmx/app/access/auth"
	"rllmx/app/access/identity"
	apphttp "rllmx/app/http"
	"rllmx/app/http/middleware"
	"rllmx/tests"
)

// standardUser is a signed-in standard user, so every refusal below is the role
// gate's and not a missing session.
type standardUser struct{}

func (standardUser) Session(*nethttp.Request) bool { return true }
func (standardUser) SessionInfo(*nethttp.Request) (auth.Session, bool) {
	return auth.Session{Role: auth.RoleUser, UserID: "u1"}, true
}
func (standardUser) APIKey(*nethttp.Request) identity.KeyResult { return identity.KeyResult{} }
func (standardUser) RequireLogin(context.Context) bool          { return true }
func (standardUser) RequireAPIKey(context.Context) bool         { return true }

// userSurface is every dashboard call a standard user may make, written out
// here on purpose: deriving it from the gate's own table would let a widened
// gate pass its own test.
var userSurface = map[string]bool{
	"GET /api/keys":                   true,
	"GET /api/keys/x":                 true,
	"PUT /api/keys/x":                 true,
	"POST /api/keys/x/reveal":         true,
	"GET /api/usage/stats":            true,
	"GET /api/usage/chart":            true,
	"GET /api/usage/history":          true,
	"GET /api/usage/logs":             true,
	"GET /api/usage/request-logs":     true,
	"GET /api/usage/providers":        true,
	"POST /api/auth/logout":           true,
	"POST /api/auth/change-password":  true,
	"GET /api/settings/require-login": true,
	"POST /api/locale":                true,
}

// dashboardCalls is every protected dashboard route in the pinned table, with
// its path parameters filled in.
func dashboardCalls(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, line := range tableOf(t) {
		fields := strings.Fields(line)
		method, path, class := fields[0], fields[1], fields[2]
		if method == nethttp.MethodHead || !strings.HasPrefix(path, "/api/") || !apphttp.IsDashboardAPI(path) {
			continue
		}
		if class != "class=2" && class != "class=3" {
			continue
		}
		var b strings.Builder
		for _, seg := range strings.Split(path, "/") {
			if b.Len() > 0 || seg != "" {
				b.WriteString("/")
			}
			switch {
			case strings.HasPrefix(seg, "{"), strings.HasPrefix(seg, "*"):
				b.WriteString("x")
			default:
				b.WriteString(seg)
			}
		}
		out = append(out, method+" "+strings.Replace(b.String(), "//", "/", 1))
	}
	return out
}

func serve(t *testing.T, a apphttp.Authenticator, call string) *httptest.ResponseRecorder {
	t.Helper()
	r := tests.NewRoute(t)
	g := apphttp.NewGuard(a, true)
	r.SetGlobalMiddleware([]contractshttp.Middleware{&middleware.RequestID{}, &middleware.Guard{Guard: g}})
	Web(r, g, fullBase())
	method, path, _ := strings.Cut(call, " ")
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = "203.0.113.9:5000"
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestAStandardUserIsRefusedEveryDashboardRouteOutsideTheirSurface(t *testing.T) {
	calls := dashboardCalls(t)
	if len(calls) < 60 {
		t.Fatalf("only %d dashboard routes found; the table walk is broken", len(calls))
	}
	for _, call := range calls {
		if userSurface[call] {
			continue
		}
		if rec := serve(t, standardUser{}, call); rec.Code != nethttp.StatusForbidden {
			t.Errorf("%s answered a standard user with %d", call, rec.Code)
		}
	}
}

func TestAnAnonymousRemoteCallerReachesNoDashboardRoute(t *testing.T) {
	for _, call := range dashboardCalls(t) {
		if rec := serve(t, nil, call); rec.Code != nethttp.StatusUnauthorized && rec.Code != nethttp.StatusForbidden {
			t.Errorf("%s answered an anonymous remote caller with %d", call, rec.Code)
		}
	}
}
