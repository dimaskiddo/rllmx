package controllers

import (
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	contractshttp "github.com/goravel/framework/contracts/http"
	goravelgin "github.com/goravel/gin"

	"rllmx/app/access/auth"
	"rllmx/app/access/identity"
	apphttp "rllmx/app/http"
	"rllmx/app/http/middleware"
	"rllmx/app/store/repository"
	"rllmx/tests"
	"rllmx/tests/store"
)

// authRig is the stack the server installs, over a real store and the real
// auth routes.
type authRig struct {
	route *goravelgin.Route
	guard *apphttp.Guard
	sq    *store.Store
}

var rigSecret = []byte("0123456789abcdef0123456789abcdef")

func newAuthRig(t *testing.T) *authRig {
	t.Helper()
	sq := store.Migrated(t)
	db := func() *repository.Store { return sq.Store }
	codec := auth.NewKeys(db, nil, identity.NewKeys(""))
	a := auth.NewAuthenticator(auth.NewStore(db), codec, bcryptOnly{}, "", func() []byte { return rigSecret }, true, false)
	trust := apphttp.NewTrust(nil)

	r := tests.NewRoute(t)
	g := apphttp.NewGuard(a, true)
	r.SetGlobalMiddleware([]contractshttp.Middleware{
		&middleware.RequestID{}, &middleware.SecurityHeaders{Trust: trust},
		&middleware.SameOrigin{}, &middleware.Guard{Guard: g},
	})
	c := NewAuthController(a, trust)
	g.Handle(r, apphttp.ClassPublic, nethttp.MethodPost, "/api/auth/login", c.Login)
	g.Handle(r, apphttp.ClassPublic, nethttp.MethodPost, "/api/auth/logout", c.Logout)
	g.Handle(r, apphttp.ClassPublic, nethttp.MethodGet, "/api/auth/status", c.Status)
	g.Handle(r, apphttp.ClassPublic, nethttp.MethodGet, "/api/settings/require-login", c.RequireLogin)
	g.Handle(r, apphttp.ClassAlwaysProtected, nethttp.MethodPost, "/api/auth/change-password", c.ChangePassword)
	return &authRig{route: r, guard: g, sq: sq}
}

// bcryptOnly is the framework hasher shape without an application, so the route
// tests never spend a real bcrypt round.
type bcryptOnly struct{}

func (bcryptOnly) Make(v string) (string, error) { return "hashed:" + v, nil }
func (bcryptOnly) Check(v, h string) bool        { return h == "hashed:"+v }
func (bcryptOnly) NeedsRehash(string) bool       { return false }

const (
	localPeer  = "127.0.0.1:5000"
	localHost  = "localhost:20128"
	remotePeer = "203.0.113.9:5000"
	remoteHost = "gw.example.com"
)

func (r *authRig) call(method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	return r.callAs(method, path, body, hdr, localPeer, localHost)
}

func (r *authRig) callAs(method, path, body string, hdr map[string]string, peer, host string) *httptest.ResponseRecorder {
	var rdr *strings.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	var req *nethttp.Request
	if rdr != nil {
		req = httptest.NewRequest(method, path, rdr)
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	req.RemoteAddr, req.Host = peer, host
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	r.route.ServeHTTP(rec, req)
	return rec
}

func cookieHeader(rec *httptest.ResponseRecorder) map[string]string {
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.CookieName && c.Value != "" {
			return map[string]string{"Cookie": auth.CookieName + "=" + c.Value}
		}
	}
	return nil
}

func TestTheAuthRoutesAreClassified(t *testing.T) {
	r := newAuthRig(t)
	for _, tc := range []struct {
		method, path string
		want         apphttp.Class
	}{
		{"POST", "/api/auth/login", apphttp.ClassPublic},
		{"POST", "/api/auth/logout", apphttp.ClassPublic},
		{"GET", "/api/auth/status", apphttp.ClassPublic},
		{"GET", "/api/settings/require-login", apphttp.ClassPublic},
		{"POST", "/api/auth/change-password", apphttp.ClassAlwaysProtected},
	} {
		class, ok := r.guard.RouteClass(tc.method, tc.path)
		if !ok {
			t.Errorf("%s %s is not in the guard table", tc.method, tc.path)
			continue
		}
		if class != tc.want {
			t.Errorf("%s %s is class %d, want %d", tc.method, tc.path, class, tc.want)
		}
	}
}

func TestLoginAcceptsTheInitialPasswordLocally(t *testing.T) {
	r := newAuthRig(t)
	rec := r.call(nethttp.MethodPost, "/api/auth/login", `{"password":"123456"}`, nil)
	if rec.Code != nethttp.StatusOK || !strings.Contains(rec.Body.String(), `"success":true`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != auth.CookieName || !cookies[0].HttpOnly || cookies[0].SameSite != nethttp.SameSiteLaxMode || cookies[0].Path != "/" {
		t.Fatalf("cookies %+v", cookies)
	}
}

func TestARemoteLoginOnAPublicDefaultGetsNoSession(t *testing.T) {
	r := newAuthRig(t)
	rec := r.callAs(nethttp.MethodPost, "/api/auth/login", `{"password":"123456"}`, nil, remotePeer, remoteHost)
	if rec.Code != nethttp.StatusForbidden || !strings.Contains(rec.Body.String(), `"mustChangePassword":true`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if hdr := cookieHeader(rec); hdr != nil {
		t.Fatal("a session cookie was issued for a remote default-password login")
	}
}

func TestFiveWrongPasswordsLockTheAddressAndACorrectOneWaitsToo(t *testing.T) {
	r := newAuthRig(t)
	for i := 1; i < auth.FreeAttempts; i++ {
		if rec := r.call(nethttp.MethodPost, "/api/auth/login", `{"password":"wrong"}`, nil); rec.Code != nethttp.StatusUnauthorized {
			t.Fatalf("attempt %d = %d", i, rec.Code)
		}
	}
	rec := r.call(nethttp.MethodPost, "/api/auth/login", `{"password":"wrong"}`, nil)
	if rec.Code != nethttp.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("the fifth wrong password: %d retry=%q", rec.Code, rec.Header().Get("Retry-After"))
	}
	if rec := r.call(nethttp.MethodPost, "/api/auth/login", `{"password":"123456"}`, nil); rec.Code != nethttp.StatusTooManyRequests {
		t.Fatalf("a correct password got through a lock: %d", rec.Code)
	}
}

func TestASuccessClearsTheFailureCount(t *testing.T) {
	r := newAuthRig(t)
	for range auth.FreeAttempts - 1 {
		r.call(nethttp.MethodPost, "/api/auth/login", `{"password":"wrong"}`, nil)
	}
	if rec := r.call(nethttp.MethodPost, "/api/auth/login", `{"password":"123456"}`, nil); rec.Code != nethttp.StatusOK {
		t.Fatalf("a correct password before the lock: %d", rec.Code)
	}
	for i := 1; i < auth.FreeAttempts; i++ {
		if rec := r.call(nethttp.MethodPost, "/api/auth/login", `{"password":"wrong"}`, nil); rec.Code != nethttp.StatusUnauthorized {
			t.Fatalf("attempt %d after a success = %d: the history was not cleared", i, rec.Code)
		}
	}
}

func TestTheSessionWorksAndLogoutEverywhereEndsIt(t *testing.T) {
	r := newAuthRig(t)
	hdr := cookieHeader(r.call(nethttp.MethodPost, "/api/auth/login", `{"password":"123456"}`, nil))
	if hdr == nil {
		t.Fatal("no session cookie")
	}

	if rec := r.call(nethttp.MethodGet, "/api/auth/status", "", hdr); !strings.Contains(rec.Body.String(), `"authenticated":true`) {
		t.Fatalf("status %q", rec.Body.String())
	}
	if rec := r.call(nethttp.MethodPost, "/api/auth/logout", `{"everywhere":true}`, hdr); rec.Code != nethttp.StatusOK {
		t.Fatalf("logout %d", rec.Code)
	}
	if rec := r.call(nethttp.MethodGet, "/api/auth/status", "", hdr); !strings.Contains(rec.Body.String(), `"authenticated":false`) {
		t.Fatalf("the session survived logout everywhere: %q", rec.Body.String())
	}
}

func TestLogoutWithoutAFlagLeavesOtherSessionsAlone(t *testing.T) {
	r := newAuthRig(t)
	hdr := cookieHeader(r.call(nethttp.MethodPost, "/api/auth/login", `{"password":"123456"}`, nil))
	other := cookieHeader(r.call(nethttp.MethodPost, "/api/auth/login", `{"password":"123456"}`, nil))
	r.call(nethttp.MethodPost, "/api/auth/logout", `{}`, hdr)
	if rec := r.call(nethttp.MethodGet, "/api/auth/status", "", other); !strings.Contains(rec.Body.String(), `"authenticated":true`) {
		t.Fatal("a plain logout ended another session")
	}
}

func TestChangingThePasswordEndsEverySession(t *testing.T) {
	r := newAuthRig(t)
	old := cookieHeader(r.call(nethttp.MethodPost, "/api/auth/login", `{"password":"123456"}`, nil))
	if rec := r.call(nethttp.MethodPost, "/api/auth/change-password",
		`{"currentPassword":"123456","newPassword":"a-much-better-passphrase"}`, old); rec.Code != nethttp.StatusOK {
		t.Fatalf("change %d %s", rec.Code, rec.Body.String())
	}
	if rec := r.call(nethttp.MethodGet, "/api/auth/status", "", old); !strings.Contains(rec.Body.String(), `"authenticated":false`) {
		t.Fatalf("the old session survives a password change: %q", rec.Body.String())
	}
	if rec := r.call(nethttp.MethodPost, "/api/auth/login", `{"password":"a-much-better-passphrase"}`, nil); rec.Code != nethttp.StatusOK {
		t.Fatalf("the new password was refused: %d", rec.Code)
	}
}

func TestChangingThePasswordJudgesTheNewOne(t *testing.T) {
	r := newAuthRig(t)
	hdr := cookieHeader(r.call(nethttp.MethodPost, "/api/auth/login", `{"password":"123456"}`, nil))
	for _, bad := range []string{"123456", "change-me", "short"} {
		rec := r.call(nethttp.MethodPost, "/api/auth/change-password",
			`{"currentPassword":"123456","newPassword":"`+bad+`"}`, hdr)
		if rec.Code != nethttp.StatusBadRequest {
			t.Errorf("%q accepted: %d", bad, rec.Code)
		}
	}
}

func TestChangingThePasswordNeedsTheCurrentOne(t *testing.T) {
	r := newAuthRig(t)
	hdr := cookieHeader(r.call(nethttp.MethodPost, "/api/auth/login", `{"password":"123456"}`, nil))
	rec := r.call(nethttp.MethodPost, "/api/auth/change-password",
		`{"currentPassword":"wrong","newPassword":"a-much-better-passphrase"}`, hdr)
	if rec.Code != nethttp.StatusUnauthorized {
		t.Fatalf("%d", rec.Code)
	}
}

func TestStatusHidesThePasswordFactsFromAnAnonymousRemoteCaller(t *testing.T) {
	r := newAuthRig(t)
	remote := r.callAs(nethttp.MethodGet, "/api/auth/status", "", nil, remotePeer, remoteHost)
	for _, leak := range []string{"hasPassword", "defaultPassword"} {
		if strings.Contains(remote.Body.String(), leak) {
			t.Errorf("%s is visible remotely: %q", leak, remote.Body.String())
		}
	}
	if local := r.call(nethttp.MethodGet, "/api/auth/status", "", nil); !strings.Contains(local.Body.String(), "hasPassword") {
		t.Fatalf("a local caller cannot see the password facts: %q", local.Body.String())
	}
}

func TestRequireLoginAnswersTheFlagAlone(t *testing.T) {
	r := newAuthRig(t)
	rec := r.call(nethttp.MethodGet, "/api/settings/require-login", "", nil)
	if rec.Code != nethttp.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"requireLogin":true}` {
		t.Fatalf("%d %q", rec.Code, rec.Body.String())
	}
}

func TestACrossSitePasswordChangeIsRefusedBeforeTheRoute(t *testing.T) {
	r := newAuthRig(t)
	rec := r.call(nethttp.MethodPost, "/api/auth/change-password",
		`{"currentPassword":"123456","newPassword":"a-much-better-passphrase"}`,
		map[string]string{"Sec-Fetch-Site": "cross-site"})
	if rec.Code != nethttp.StatusForbidden {
		t.Fatalf("%d: the cross-site guard did not run before the route", rec.Code)
	}
}

func TestTheLoginRouteNeedsNoKeyAndLeaksNoPassword(t *testing.T) {
	r := newAuthRig(t)
	rec := r.call(nethttp.MethodPost, "/api/auth/login", `{"password":"wrong"}`, nil)
	if strings.Contains(rec.Body.String(), "wrong") || strings.Contains(rec.Body.String(), "123456") {
		t.Fatalf("the answer repeats a password: %q", rec.Body.String())
	}
}
