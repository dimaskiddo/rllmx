package http

import (
	"context"
	"crypto/tls"
	"io"
	"maps"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"rllmx/app/settings"
	"rllmx/tests"
)

func target(srv *httptest.Server) HealthTarget { return HealthTarget{URL: srv.URL + "/api/health"} }

func respond(status int, body string) *httptest.Server {
	return httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
}

func TestProbeAcceptsOnly200WithOkTrue(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		ok     bool
	}{
		{"healthy", 200, `{"ok":true}`, true},
		{"healthy with spacing", 200, ` { "ok" : true } `, true},
		{"ok false", 200, `{"ok":false}`, false},
		{"ok missing", 200, `{}`, false},
		{"ok as a string", 200, `{"ok":"true"}`, false},
		{"not json", 200, `ok`, false},
		{"empty body", 200, ``, false},
		{"server error", 500, `{"ok":true}`, false},
		{"unauthorized", 401, `{"ok":true}`, false},
		{"no content", 204, ``, false},
	}
	for _, c := range cases {
		srv := respond(c.status, c.body)
		err := Probe(context.Background(), target(srv))
		srv.Close()
		if (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok=%v", c.name, err, c.ok)
		}
	}
}

func TestProbeFollowsAtMostThreeRedirects(t *testing.T) {
	// A relocation to the health path is what a load balancer does, so up to
	// three hops are followed. A fourth is refused, so a redirect loop cannot
	// hold the probe open.
	var hits int
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		hits++
		switch r.URL.Path {
		case "/api/health":
			_, _ = io.WriteString(w, `{"ok":true}`)
		case "/hop1", "/hop2", "/hop3":
			nethttp.Redirect(w, r, "/"+next(r.URL.Path), nethttp.StatusFound)
		default:
			_, _ = io.WriteString(w, "moved")
		}
	}))
	defer srv.Close()

	target := HealthTarget{URL: srv.URL + "/hop1"}
	if err := Probe(context.Background(), target); err != nil {
		// /hop1 -> /hop2 -> /hop3 -> /api/health is three hops and must work.
		t.Fatalf("three redirects were refused: %v", err)
	}

	hits = 0
	loop := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		hits++
		nethttp.Redirect(w, r, "/loop", nethttp.StatusFound)
	}))
	defer loop.Close()
	if err := Probe(context.Background(), HealthTarget{URL: loop.URL + "/loop"}); err == nil {
		t.Fatal("a redirect loop counted as healthy")
	}
	if hits > 4 {
		t.Fatalf("%d requests: the redirect limit did not stop the loop", hits)
	}
}

// next names the path one hop further along.
func next(p string) string {
	switch p {
	case "/hop1":
		return "hop2"
	case "/hop2":
		return "hop3"
	}
	return "api/health"
}

func TestProbeReadsAtMostFourKiB(t *testing.T) {
	srv := respond(200, `{"ok":true}`+strings.Repeat(" ", 1<<20))
	defer srv.Close()
	if err := Probe(context.Background(), target(srv)); err != nil {
		t.Fatalf("a valid object followed by padding: %v", err)
	}
	srv2 := respond(200, strings.Repeat(" ", 5000)+`{"ok":true}`)
	defer srv2.Close()
	if err := Probe(context.Background(), target(srv2)); err == nil {
		t.Fatal("a body whose object starts past the cap was accepted")
	}
}

func TestProbeStopsWhenTheContextEnds(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	begin := time.Now()
	if err := Probe(ctx, target(srv)); err == nil {
		t.Fatal("a hanging server counted as healthy")
	}
	if took := time.Since(begin); took > 2*time.Second {
		t.Fatalf("took %v", took)
	}
}

func TestProbeFailsWhenNothingListens(t *testing.T) {
	srv := respond(200, `{"ok":true}`)
	u := target(srv)
	srv.Close()
	if err := Probe(context.Background(), u); err == nil {
		t.Fatal("a closed port counted as healthy")
	}
}

// net/http never proxies a loopback target, so a live request to a test server
// cannot show whether the proxy variables are read. The transport is checked
// instead: it must have no proxy function at all, and a request to a name that
// the environment would proxy must resolve to no proxy.
func TestProbeClientIgnoresTheProxyEnvironment(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://proxy.invalid:3128")
	t.Setenv("HTTPS_PROXY", "http://proxy.invalid:3128")
	t.Setenv("ALL_PROXY", "http://proxy.invalid:3128")
	tr, ok := probeClient(HealthTarget{}).Transport.(*nethttp.Transport)
	if !ok || tr.Proxy != nil {
		t.Fatal("the probe transport has a proxy function")
	}
	req, _ := nethttp.NewRequest(nethttp.MethodGet, "http://gw.internal.example/api/health", nil)
	if u, err := nethttp.ProxyFromEnvironment(req); err != nil || u == nil {
		t.Skipf("this environment does not proxy the sample name (%v, %v), so the check above is the only one", u, err)
	}
}

func TestProbeClientKeepsNoConnectionAndLimitsRedirects(t *testing.T) {
	c := probeClient(HealthTarget{URL: "http://127.0.0.1:1/api/health"})
	if !c.Transport.(*nethttp.Transport).DisableKeepAlives {
		t.Error("a one-shot check must not leave an idle connection behind")
	}
	req, _ := nethttp.NewRequest(nethttp.MethodGet, "http://127.0.0.1:1/x", nil)
	many := make([]*nethttp.Request, 4)
	for i := range many {
		many[i] = req
	}
	if err := c.CheckRedirect(req, many); err != nethttp.ErrUseLastResponse {
		t.Errorf("a fourth hop = %v, want refusal", err)
	}
	if err := c.CheckRedirect(req, many[:3]); err != nil {
		t.Errorf("a third hop = %v, want follow", err)
	}
}

func TestProbeVerifiesTLS(t *testing.T) {
	srv := httptest.NewTLSServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()
	pool := srv.Client().Transport.(*nethttp.Transport).TLSClientConfig.RootCAs

	good := HealthTarget{URL: srv.URL + "/api/health", TLS: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
	if err := Probe(context.Background(), good); err != nil {
		t.Fatalf("a trusted certificate: %v", err)
	}
	if err := Probe(context.Background(), HealthTarget{URL: srv.URL + "/api/health"}); err == nil {
		t.Fatal("an unknown certificate was accepted: TLS must always be verified")
	}
}

func TestHealthTargetOf(t *testing.T) {
	load := func(over map[string]any) *settings.Server {
		m := tests.ServerValues()
		maps.Copy(m, over)
		s, err := settings.LoadServer(tests.Config{Values: m})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	cases := []struct {
		name string
		over map[string]any
		url  string
		tls  string
	}{
		{"default", nil, "http://127.0.0.1:20128/api/health", ""},
		{"wildcard v4", map[string]any{"http.host": "0.0.0.0"}, "http://127.0.0.1:20128/api/health", ""},
		{"wildcard v6", map[string]any{"http.host": "::"}, "http://[::1]:20128/api/health", ""},
		{"empty host", map[string]any{"http.host": ""}, "http://127.0.0.1:20128/api/health", ""},
		{"named host", map[string]any{"http.host": "10.1.2.3", "http.port": "8080"}, "http://10.1.2.3:8080/api/health", ""},
		{"ipv6 host", map[string]any{"http.host": "::1"}, "http://[::1]:20128/api/health", ""},
		{"tls uses the host name", map[string]any{"http.host": "gw.internal", "http.tls.ssl.cert": "c", "http.tls.ssl.key": "k"}, "https://gw.internal:20128/api/health", "gw.internal"},
		{"tls prefers the public URL name", map[string]any{"http.url": "https://gw.example.com", "http.tls.ssl.cert": "c", "http.tls.ssl.key": "k"}, "https://127.0.0.1:20128/api/health", "gw.example.com"},
	}
	for _, c := range cases {
		got := HealthTargetOf(load(c.over))
		if got.URL != c.url {
			t.Errorf("%s: url %q, want %q", c.name, got.URL, c.url)
		}
		if (c.tls == "") != (got.TLS == nil) || (got.TLS != nil && (got.TLS.ServerName != c.tls || got.TLS.MinVersion < tls.VersionTLS12)) {
			t.Errorf("%s: tls %+v, want server name %q", c.name, got.TLS, c.tls)
		}
	}
}
