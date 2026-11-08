package settings

import (
	"maps"
	"net/netip"
	"strings"
	"testing"
	"time"

	"rllmx/tests"
)

func loadWith(t *testing.T, over map[string]any) (*Server, error) {
	t.Helper()
	m := tests.ServerValues()
	maps.Copy(m, over)
	return LoadServer(tests.Config{Values: m})
}

func TestLoadServerDefaults(t *testing.T) {
	s, err := loadWith(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.Host != "127.0.0.1" || s.Port != 20128 || s.URL != "" {
		t.Errorf("listener = %+v", s)
	}
	if s.ReadHeaderTimeout != 10*time.Second || s.IdleTimeout != 120*time.Second || s.ShutdownGrace != 30*time.Second {
		t.Errorf("durations = %+v", s)
	}
	if s.HeaderLimitKiB != 64 || s.MaxBodyMB != 128 || !s.TrustLoopback || len(s.TrustedProxies) != 0 {
		t.Errorf("limits = %+v", s)
	}
	if s.CertFile != "" || s.KeyFile != "" {
		t.Errorf("tls = %+v", s)
	}
}

func TestServerDurationBounds(t *testing.T) {
	cases := []struct {
		key     string
		env     string
		ok, bad []string
	}{
		{"http.server.read_header_timeout", "HTTP_SERVER_READ_HEADER_TIMEOUT", []string{"1s", "10s", "5m"}, []string{"0s", "999ms", "6m", "soon"}},
		{"http.server.idle_timeout", "HTTP_SERVER_IDLE_TIMEOUT", []string{"1s", "120s", "1h"}, []string{"0s", "2h", "x"}},
		{"http.server.shutdown_grace", "HTTP_SERVER_SHUTDOWN_GRACE", []string{"1s", "45s", "10m"}, []string{"0s", "11m", "x"}},
	}
	for _, c := range cases {
		for _, v := range c.ok {
			if _, err := loadWith(t, map[string]any{c.key: v}); err != nil {
				t.Errorf("%s=%s rejected: %v", c.env, v, err)
			}
		}
		for _, v := range c.bad {
			_, err := loadWith(t, map[string]any{c.key: v})
			if err == nil || !strings.Contains(err.Error(), c.env) {
				t.Errorf("%s=%s: error = %v", c.env, v, err)
			}
			if err != nil && strings.Contains(err.Error(), v) && len(v) > 3 {
				t.Errorf("%s=%s: the error quotes the value: %v", c.env, v, err)
			}
		}
	}
}

func TestServerPortAndBodyBounds(t *testing.T) {
	for _, v := range []string{"1", "20128", "65535"} {
		if _, err := loadWith(t, map[string]any{"http.port": v}); err != nil {
			t.Errorf("port %s rejected: %v", v, err)
		}
	}
	for _, v := range []string{"0", "65536", "-1", "http"} {
		if _, err := loadWith(t, map[string]any{"http.port": v}); err == nil || !strings.Contains(err.Error(), "APP_PORT") {
			t.Errorf("port %s: error = %v", v, err)
		}
	}
	for _, v := range []int{0, -5, 1<<20 + 1} {
		if _, err := loadWith(t, map[string]any{"http.guard.max_body_mb": v}); err == nil || !strings.Contains(err.Error(), "SERVER_MAX_BODY_MB") {
			t.Errorf("max body %d: error = %v", v, err)
		}
	}
	if s, err := loadWith(t, map[string]any{"http.guard.max_body_mb": 7, "http.guard.trust_loopback": false}); err != nil || s.MaxBodyMB != 7 || s.TrustLoopback {
		t.Errorf("got %+v, %v", s, err)
	}
}

func TestTLSFilesComeInPairs(t *testing.T) {
	both := map[string]any{"http.tls.ssl.cert": "/etc/ssl/cert.pem", "http.tls.ssl.key": "/etc/ssl/key.pem"}
	if s, err := loadWith(t, both); err != nil || s.CertFile != "/etc/ssl/cert.pem" || s.KeyFile != "/etc/ssl/key.pem" {
		t.Fatalf("got %+v, %v", s, err)
	}
	for _, one := range []map[string]any{{"http.tls.ssl.cert": "/etc/ssl/cert.pem"}, {"http.tls.ssl.key": "/etc/ssl/key.pem"}} {
		if _, err := loadWith(t, one); err == nil || !strings.Contains(err.Error(), "http.tls.ssl") {
			t.Errorf("%v: error = %v", one, err)
		}
	}
}

func TestTrustedProxiesParse(t *testing.T) {
	s, err := loadWith(t, map[string]any{"http.guard.trusted_proxies": " 10.0.0.0/8 , 192.168.1.5 ,fd00::/8"})
	if err != nil {
		t.Fatal(err)
	}
	want := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("192.168.1.5/32"), netip.MustParsePrefix("fd00::/8")}
	if len(s.TrustedProxies) != 3 {
		t.Fatalf("got %v", s.TrustedProxies)
	}
	for i, w := range want {
		if s.TrustedProxies[i] != w {
			t.Errorf("entry %d = %v, want %v", i, s.TrustedProxies[i], w)
		}
	}
	for _, bad := range []string{"not-an-ip", "10.0.0.0/33", "1.2.3.4,host.example.com"} {
		_, err := loadWith(t, map[string]any{"http.guard.trusted_proxies": bad})
		if err == nil || !strings.Contains(err.Error(), "SERVER_TRUSTED_PROXIES") || strings.Contains(err.Error(), "not-an-ip") {
			t.Errorf("%q: error = %v", bad, err)
		}
	}
}

func TestPublicURLRules(t *testing.T) {
	good := map[string]string{
		"https://gw.example.com":      "https://gw.example.com",
		"https://gw.example.com/":     "https://gw.example.com",
		"https://gw.example.com:8443": "https://gw.example.com:8443",
		"http://localhost:20128":      "http://localhost:20128",
		"http://127.0.0.1:20128":      "http://127.0.0.1:20128",
		"http://[::1]:20128":          "http://[::1]:20128",
		"HTTPS://GW.Example.com":      "https://gw.example.com",
		"":                            "",
	}
	for in, want := range good {
		s, err := loadWith(t, map[string]any{"http.url": in})
		if err != nil {
			t.Errorf("%q rejected: %v", in, err)
			continue
		}
		if s.URL != want {
			t.Errorf("%q -> %q, want %q", in, s.URL, want)
		}
	}
	for _, bad := range []string{
		"http://gw.example.com", "ftp://x.example.com", "https://", "gw.example.com", "https://u:p@gw.example.com",
		"https://gw.example.com/path", "https://gw.example.com?x=1", "https://gw.example.com#f", "javascript:alert(1)",
	} {
		_, err := loadWith(t, map[string]any{"http.url": bad})
		if err == nil || !strings.Contains(err.Error(), "APP_URL") {
			t.Errorf("%q: error = %v", bad, err)
		}
	}
}

func TestServerErrorsNeverQuoteTheValue(t *testing.T) {
	const marker = "s3cr3t-marker"
	for key, env := range map[string]string{
		"http.url":                   "APP_URL",
		"http.guard.trusted_proxies": "SERVER_TRUSTED_PROXIES",
	} {
		_, err := loadWith(t, map[string]any{key: "https://u:" + marker + "@" + marker})
		if err == nil || strings.Contains(err.Error(), marker) || !strings.Contains(err.Error(), env) {
			t.Errorf("%s: error = %v", env, err)
		}
	}
}
