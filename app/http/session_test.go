package http

import (
	"context"
	"errors"
	nethttp "net/http"
	"strings"
	"testing"

	contractshttp "github.com/goravel/framework/contracts/http"

	"rllmx/app/access/auth"
)

func TestRoleAllowsTheAdminEverythingAndTheUserTheirOwnSurface(t *testing.T) {
	cases := []struct {
		role, method, path string
		want               bool
	}{
		{auth.RoleAdmin, nethttp.MethodGet, "/api/providers", true},
		{auth.RoleAdmin, nethttp.MethodDelete, "/api/keys/k1", true},
		{auth.RoleUser, nethttp.MethodGet, "/api/auth/status", true},
		{auth.RoleUser, nethttp.MethodPost, "/api/auth/logout", true},
		{auth.RoleUser, nethttp.MethodPost, "/api/auth/change-password", true},
		{auth.RoleUser, nethttp.MethodGet, "/api/keys", true},
		{auth.RoleUser, nethttp.MethodGet, "/api/keys/k1", true},
		{auth.RoleUser, nethttp.MethodGet, "/api/about/update", true},
		{auth.RoleUser, nethttp.MethodPost, "/api/about/upgrade", false},
		{auth.RoleUser, nethttp.MethodPut, "/api/keys/k1", true},
		{auth.RoleUser, nethttp.MethodPost, "/api/keys/k1/reveal", true},
		{auth.RoleUser, nethttp.MethodGet, "/api/keys/k1/reveal", false},
		{auth.RoleUser, nethttp.MethodPost, "/api/keys/k1/other", false},
		{auth.RoleUser, nethttp.MethodGet, "/api/usage/stats", true},
		{auth.RoleUser, nethttp.MethodGet, "/api/usage/chart", true},
		{auth.RoleUser, nethttp.MethodGet, "/api/usage/history", true},
		{auth.RoleUser, nethttp.MethodGet, "/api/usage/logs", true},
		{auth.RoleUser, nethttp.MethodGet, "/api/usage/providers", true},
		{auth.RoleUser, nethttp.MethodGet, "/api/providers", false},
		{auth.RoleUser, nethttp.MethodPost, "/api/keys", false},
		{auth.RoleUser, nethttp.MethodDelete, "/api/keys/k1", false},
		{auth.RoleUser, nethttp.MethodGet, "/api/keys/k1/allow", false},
		{auth.RoleUser, nethttp.MethodGet, "/api/keys/", false},
		{auth.RoleUser, nethttp.MethodPost, "/api/settings", false},
		{auth.RoleUser, nethttp.MethodGet, "/api/usage/stream", false},
		{auth.RoleUser, nethttp.MethodGet, "/api/usage/k1", false},
		{auth.RoleUser, nethttp.MethodDelete, "/api/providers/k1", false},
		{auth.RoleUser, nethttp.MethodPost, "/api/locale", true},
		{auth.RoleUser, nethttp.MethodGet, "/dashboard/endpoint", true},
		{auth.RoleUser, nethttp.MethodGet, "/static/js/app.js", true},
	}
	for _, c := range cases {
		if got := roleAllows(c.role, c.method, c.path); got != c.want {
			t.Errorf("%s %s %s = %v, want %v", c.role, c.method, c.path, got, c.want)
		}
	}
}

func TestAStandardUserIsRefusedEveryRouteOutsideTheirSurface(t *testing.T) {
	s := guardApp(t, fakeAuth{session: true, role: auth.RoleUser}, true)
	role := func(ctx contractshttp.Context) contractshttp.Response {
		who, ok := SessionFrom(ctx)
		if !ok || who.Role != auth.RoleUser {
			return ctx.Response().String(nethttp.StatusInternalServerError, "no session")
		}
		return ctx.Response().String(nethttp.StatusOK, who.Role)
	}
	s.guard.Handle(s.route, ClassProtected, "GET", "/api/usage/stats", role)
	s.guard.Handle(s.route, ClassAlwaysProtected, "GET", "/api/keys", reached)
	s.guard.Handle(s.route, ClassAlwaysProtected, "GET", "/api/keys/{id}", reached)
	s.guard.Handle(s.route, ClassAlwaysProtected, "PUT", "/api/keys/{id}", reached)
	s.guard.Handle(s.route, ClassAlwaysProtected, "DELETE", "/api/keys/{id}", reached)
	s.guard.Handle(s.route, ClassAlwaysProtected, "GET", "/api/providers", reached)

	for _, c := range []struct {
		method, path string
		want         int
	}{
		{"GET", "/api/usage/stats", 200},
		{"GET", "/api/keys", 200},
		{"GET", "/api/keys/k1", 200},
		{"PUT", "/api/keys/k1", 200},
		{"DELETE", "/api/keys/k1", 403},
		{"GET", "/api/providers", 403},
		{"GET", "/api/prot", 403},
		{"POST", "/api/prot", 403},
		{"GET", "/api/always", 403},
		{"GET", "/dashboard/home", 200},
	} {
		if rec := s.call(remotePeer, c.method, c.path, nil); rec.Code != c.want {
			t.Errorf("%s %s = %d, want %d: %s", c.method, c.path, rec.Code, c.want, strings.TrimSpace(rec.Body.String()))
		}
	}
	if rec := s.call(remotePeer, "GET", "/api/usage/stats", nil); !strings.Contains(rec.Body.String(), auth.RoleUser) {
		t.Errorf("the handler saw no session role: %s", rec.Body.String())
	}
}

func TestTheAdminStillReachesEveryRoute(t *testing.T) {
	s := guardApp(t, fakeAuth{session: true}, true)
	for _, path := range []string{"/api/prot", "/api/always"} {
		if rec := s.call(remotePeer, "GET", path, nil); rec.Code != 200 {
			t.Errorf("%s = %d", path, rec.Code)
		}
	}
}

type keyLister struct {
	keys []string
	err  error
}

func (k keyLister) KeysOf(context.Context, string) ([]string, error) { return k.keys, k.err }

func TestScopedKeysSaysWhoSeesWhat(t *testing.T) {
	cases := []struct {
		name    string
		session *auth.Session
		lister  KeyLister
		scoped  bool
		want    []string
		failed  bool
	}{
		{"no session is the admin", nil, nil, false, nil, false},
		{"an admin is not narrowed", &auth.Session{Role: auth.RoleAdmin}, nil, false, nil, false},
		{"a user with no keys has none, never nil", &auth.Session{Role: auth.RoleUser, UserID: "u1"}, keyLister{}, true, []string{}, false},
		{"a user has their keys", &auth.Session{Role: auth.RoleUser, UserID: "u1"}, keyLister{keys: []string{"a"}}, true, []string{"a"}, false},
		{"a read failure is a failure", &auth.Session{Role: auth.RoleUser, UserID: "u1"}, keyLister{err: errors.New("x")}, true, nil, true},
		{"a user with no list has none", &auth.Session{Role: auth.RoleUser, UserID: "u1"}, nil, true, []string{}, false},
	}
	for _, c := range cases {
		keys, scoped, err := ScopedKeys(context.Background(), c.session, c.lister)
		if scoped != c.scoped || (err != nil) != c.failed || (keys == nil) != (c.want == nil) || len(keys) != len(c.want) {
			t.Errorf("%s: keys=%v scoped=%v err=%v", c.name, keys, scoped, err)
		}
	}
}
