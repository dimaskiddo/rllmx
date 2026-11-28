package controllers

import (
	"context"
	"errors"
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
	g.Handle(route, apphttp.ClassProtected, nethttp.MethodGet, "/api/usage/stream", r.c.Stream)
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

// streamAsUser serves only the feed, with a standard user's session in place of
// the guard, so the handler's own scoping is what answers.
func streamAsUser(t *testing.T, r *usageRig, uid string) nethttp.Handler {
	t.Helper()
	route := tests.NewRoute(t)
	route.SetGlobalMiddleware([]contractshttp.Middleware{&middleware.RequestID{}, asUser{uid: uid}})
	route.Get("/api/usage/stream", func(ctx contractshttp.Context) contractshttp.Response { return r.c.Stream(ctx) })
	return route
}

// The feed's live table names upstream accounts, which are the operator's: a
// scoped caller's frame carries empty rows where the admin's carries the table.
func TestAStandardUsersFeedFrameCarriesNoAccountRows(t *testing.T) {
	r := newUsageRig(t)
	release := r.flights.Start("openai", "gpt-4o", r.conn.ID)
	defer release(false)
	recent := []usageRecent{{Model: "gpt-4o"}}
	scoped := r.c.feedFrame([]string{r.key.ID}, nil, recent)
	if scoped["errorProvider"] != "" {
		t.Fatalf("a scoped frame names the failing provider: %v", scoped)
	}
	if active, ok := scoped["activeRequests"].([]usageActive); !ok || len(active) != 0 {
		t.Fatalf("a scoped frame carries the live table: %v", scoped)
	}
	if pending, ok := scoped["pending"].(contractshttp.Json); !ok || len(pending) != 0 {
		t.Fatalf("a scoped frame carries pending rows: %v", scoped)
	}
	if got := scoped["recentRequests"]; got == nil {
		t.Fatal("a scoped frame dropped the caller's own rows")
	}
	admin := r.c.feedFrame(nil, map[string]string{r.conn.ID: "work"}, recent)
	if active := admin["activeRequests"].([]usageActive); len(active) != 1 {
		t.Fatalf("an admin frame lost the live table: %v", admin)
	}
}

// A key list that cannot be read is not "every key": the feed refuses before it
// opens rather than widening the caller's scope.
func TestAFeedWhoseScopeCannotBeReadFailsClosed(t *testing.T) {
	r := newUsageRig(t)
	r.c.Users = failLister{}
	rec := httptest.NewRecorder()
	streamAsUser(t, r, "u1").ServeHTTP(rec, httptest.NewRequest(nethttp.MethodGet, "/api/usage/stream", nil))
	if rec.Code != nethttp.StatusServiceUnavailable {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

// failLister is a key lister that cannot answer.
type failLister struct{}

func (failLister) KeysOf(context.Context, string) ([]string, error) {
	return nil, errors.New("the store is down")
}
