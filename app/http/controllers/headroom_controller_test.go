package controllers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apphttp "rllmx/app/http"
	"rllmx/app/store/repository"
)

// sidecar is a test server standing in for the compression proxy.
func sidecar(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

// headroomRoutes registers the sidecar routes on the rig's router.
func (m *mgmtRig) headroomRoutes(c *HeadroomController) {
	m.guard.Handle(m.router, apphttp.ClassAlwaysProtected, http.MethodGet, "/api/headroom/status", c.Status)
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		m.guard.Handle(m.router, apphttp.ClassAlwaysProtected, method, "/api/headroom/proxy/*path", c.Proxy)
	}
}

// headroomRig points the sidecar address at a test server through the
// configuration default, with the stored row cleared.
func headroomRig(t *testing.T, srv *httptest.Server) *mgmtRig {
	t.Helper()
	m := newMgmtRig(t)
	if err := m.st.Store.Settings().Patch(context.Background(), map[string]string{"headroomUrl": `""`}); err != nil {
		t.Fatal(err)
	}
	m.headroomRoutes(&HeadroomController{
		DB: func() *repository.Store { return m.st.Store },
		Client: func(string) *http.Client {
			return srv.Client()
		},
		DefaultURL: srv.URL,
	})
	return m
}

func TestTheStatusRouteReportsReachability(t *testing.T) {
	srv := sidecar(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Errorf("path %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	})
	m := headroomRig(t, srv)
	code, out, raw := m.call(http.MethodGet, "/api/headroom/status", "", true)
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	if out["reachable"] != true || out["url"] != srv.URL {
		t.Fatalf("%v", out)
	}
}

func TestTheStatusRouteReportsAnAbsentSidecar(t *testing.T) {
	srv := sidecar(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) })
	m := headroomRig(t, srv)
	_, out, raw := m.call(http.MethodGet, "/api/headroom/status", "", true)
	if out["reachable"] != false {
		t.Fatalf("a refusing sidecar read as up: %s", raw)
	}
}

func TestTheProxyForwardsPathQueryAndBody(t *testing.T) {
	var gotPath, gotQuery, gotBody, gotHop string
	srv := sidecar(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery, gotHop = r.URL.Path, r.URL.RawQuery, r.Header.Get("Connection")
		b := make([]byte, 64)
		n, _ := r.Body.Read(b)
		gotBody = string(b[:n])
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Connection", "close")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	m := headroomRig(t, srv)
	code, _, raw := m.call(http.MethodPost, "/api/headroom/proxy/v1/compress?a=1", `{"x":1}`, true)
	if code != 200 || raw != `{"ok":true}` {
		t.Fatalf("%d %s", code, raw)
	}
	if gotPath != "/v1/compress" || gotQuery != "a=1" || gotBody != `{"x":1}` || gotHop != "" {
		t.Fatalf("path %q query %q body %q hop %q", gotPath, gotQuery, gotBody, gotHop)
	}
}

func TestTheProxyNeedsASession(t *testing.T) {
	srv := sidecar(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	m := headroomRig(t, srv)
	if code, _, _ := m.call(http.MethodGet, "/api/headroom/proxy/stats", "", false); code != 401 {
		t.Fatalf("unauthenticated proxy answered %d", code)
	}
}

func TestARemoteSidecarGetsNoCredentials(t *testing.T) {
	if !isLoopback("http://localhost:8787/x") || !isLoopback("http://127.0.0.1:1/x") || !isLoopback("http://[::1]:1/x") {
		t.Fatal("a local address did not read as this machine")
	}
	if isLoopback("https://sidecar.example/x") {
		t.Fatal("a remote host read as this machine")
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Cookie", "session=1")
	req.Header.Set("Authorization", "Bearer k")
	out := http.Header{}
	copyProxyHeaders(out, req.Header, isLoopback("https://sidecar.example/x"))
	if out.Get("Cookie") != "" || out.Get("Authorization") != "" {
		t.Fatalf("credentials forwarded: %v", out)
	}
	out = http.Header{}
	copyProxyHeaders(out, req.Header, isLoopback("http://127.0.0.1:1/x"))
	if out.Get("Cookie") == "" || out.Get("Authorization") == "" {
		t.Fatal("credentials withheld from a local sidecar")
	}
}

func TestTheSidecarAddressStaysOnItsBase(t *testing.T) {
	for _, c := range []struct{ base, path, want string }{
		{"http://localhost:8787", "stats", "http://localhost:8787/stats"},
		{"http://localhost:8787/", "/stats", "http://localhost:8787/stats"},
		{"http://localhost:8787/base", "stats", "http://localhost:8787/base/stats"},
	} {
		got, err := sidecarURL(c.base, c.path, "")
		if err != nil || got != c.want {
			t.Errorf("%s + %s = %q, %v", c.base, c.path, got, err)
		}
	}
	for _, base := range []string{"ftp://host", "http://u:p@host", "not a url"} {
		if got, err := sidecarURL(base, "x", ""); err == nil {
			t.Errorf("%s accepted as %q", base, got)
		}
	}
}

func TestTheMaskedAddressKeepsNoCredential(t *testing.T) {
	if got := maskURL("http://u:p@host:8787/?x=1"); strings.Contains(got, "u:p") {
		t.Fatalf("masked to %q", got)
	}
}
