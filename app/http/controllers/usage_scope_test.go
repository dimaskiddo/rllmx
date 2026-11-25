package controllers

import (
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	contractshttp "github.com/goravel/framework/contracts/http"

	"rllmx/app/access/auth"
	apphttp "rllmx/app/http"
	"rllmx/app/http/middleware"
	"rllmx/app/store/analytics"
	"rllmx/app/store/repository"
	"rllmx/tests"
)

// userUsageRoute serves the usage routes to a standard user over the rig's
// store, so the user's key list comes from the real repository.
func userUsageRoute(t *testing.T, r *usageRig, uid string) (*auth.Users, func(path string) *httptest.ResponseRecorder) {
	t.Helper()
	users := auth.NewUsers(auth.NewUserRepo(func() *repository.Store { return r.m.st.Store }, 0))
	if err := users.Put(t.Context(), auth.User{ID: uid, Username: "dimas", Hash: "hash"}); err != nil {
		t.Fatal(err)
	}
	r.c.Users = users
	route := tests.NewRoute(t)
	g := apphttp.NewGuard(signedSession{role: auth.RoleUser, uid: uid}, true)
	route.SetGlobalMiddleware([]contractshttp.Middleware{&middleware.RequestID{}, &middleware.Guard{Guard: g}})
	g.Handle(route, apphttp.ClassProtected, nethttp.MethodGet, "/api/usage/stats", r.c.Stats)
	g.Handle(route, apphttp.ClassProtected, nethttp.MethodGet, "/api/usage/history", r.c.History)
	g.Handle(route, apphttp.ClassProtected, nethttp.MethodGet, "/api/usage/logs", r.c.Logs)
	return users, func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(nethttp.MethodGet, path, nil)
		req.RemoteAddr = keysLocalPeer
		rec := httptest.NewRecorder()
		route.ServeHTTP(rec, req)
		return rec
	}
}

func TestAUserWithNoKeysReadsNoUsage(t *testing.T) {
	r := newUsageRig(t)
	a, b := r.sums()
	r.f.sums = []analytics.Sum{a, b}
	_, get := userUsageRoute(t, r, "u1")

	for _, path := range []string{"/api/usage/stats?period=all", "/api/usage/history", "/api/usage/logs"} {
		rec := get(path)
		if rec.Code != nethttp.StatusOK {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
		if r.f.lastKeys == nil {
			t.Fatalf("%s: a user with no keys asked the store for every key", path)
		}
		if len(r.f.lastKeys) != 0 {
			t.Fatalf("%s: keys %v", path, r.f.lastKeys)
		}
	}
}

func TestAUserReadsOnlyTheKeysTheyOwn(t *testing.T) {
	r := newUsageRig(t)
	users, get := userUsageRoute(t, r, "u1")
	if err := users.SetOwner(t.Context(), r.key.ID, "u1"); err != nil {
		t.Fatal(err)
	}
	if rec := get("/api/usage/stats?period=all"); rec.Code != nethttp.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if len(r.f.lastKeys) != 1 || r.f.lastKeys[0] != r.key.ID {
		t.Fatalf("keys %v", r.f.lastKeys)
	}
}

func TestAUserSeesNoNameOrEmailOfTheOperatorsUpstreamAccounts(t *testing.T) {
	r := newUsageRig(t)
	a, b := r.sums()
	r.f.sums = []analytics.Sum{a, b}
	r.f.recent = []analytics.Request{{ID: 7, Time: usageNow, Provider: "openai", Model: "gpt-4o", ConnectionID: r.conn.ID, APIKeyName: "ci", Status: 200}}
	users, get := userUsageRoute(t, r, "u1")
	if err := users.SetOwner(t.Context(), r.key.ID, "u1"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/usage/stats?period=all", "/api/usage/logs"} {
		rec := get(path)
		if rec.Code != nethttp.StatusOK {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "work") {
			t.Errorf("%s names the operator's account to a standard user: %s", path, rec.Body.String())
		}
	}
	if _, _, raw := r.m.call("GET", "/api/usage/logs", "", true); !strings.Contains(raw, "work") {
		t.Errorf("the administrator lost the account name: %s", raw)
	}
}
