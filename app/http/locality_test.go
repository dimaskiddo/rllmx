package http

import (
	nethttp "net/http"
	"net/http/httptest"
	"testing"
)

func req(remote, host string, hdr map[string]string) *nethttp.Request {
	r := httptest.NewRequest(nethttp.MethodGet, "/api/x", nil)
	r.RemoteAddr = remote
	r.Host = host
	r.Header = nethttp.Header{}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	return r
}

func TestLoopbackPeers(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:5000":            true,
		"127.1.2.3:5000":            true,
		"[::1]:5000":                true,
		"[::ffff:127.0.0.1]:5000":   true,
		"[::ffff:127.9.9.9]:5000":   true,
		"192.168.1.5:5000":          false,
		"10.0.0.1:5000":             false,
		"203.0.113.9:5000":          false,
		"[2001:db8::1]:5000":        false,
		"[::ffff:203.0.113.9]:5000": false,
		"[fe80::1%eth0]:5000":       false,
		"0.0.0.0:5000":              false,
		"[::]:5000":                 false,
		"":                          false,
		"garbage":                   false,
		"127.0.0.1":                 false,
		"localhost:5000":            false,
		"127.0.0.1:notaport":        false,
		"[::1]":                     false,
	}
	for remote, want := range cases {
		if got := loopbackPeer(remote); got != want {
			t.Errorf("loopbackPeer(%q) = %v, want %v", remote, got, want)
		}
	}
}

func TestLoopbackHost(t *testing.T) {
	cases := map[string]bool{
		"localhost": true, "localhost:20128": true, "LOCALHOST": true, "LocalHost:80": true, "localhost.": true, "localhost.:20128": true,
		"127.0.0.1": true, "127.0.0.1:20128": true, "127.5.5.5": true, "[::1]": true, "[::1]:20128": true, "::1": true,
		"[::ffff:127.0.0.1]:80": true,
		"":                      false, "evil.com": false, "localhost.evil.com": false, "evil.com:80": false, "notlocalhost": false, "localhost.localdomain": false,
		"0.0.0.0": false, "0.0.0.0:80": false, "[::]": false, "[::]:80": false, "192.168.1.5": false, "203.0.113.9:80": false,
		"user@localhost": false, "user:pw@localhost": false, "localhost@evil.com": false, "localhost:abc": false, "localhost:": false,
		"127.0.0.1.evil.com": false, "[::1": false, "::1]": false, "[fe80::1%eth0]": false, "localhost%00": false, "local host": false,
		"localhost:99999999": false, "xn--localhost": false, "1.0.0.127": false, "2130706433": false, "0x7f000001": false, "127.1": false,
	}
	for host, want := range cases {
		if got := loopbackHost(host); got != want {
			t.Errorf("loopbackHost(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestLoopbackOrigin(t *testing.T) {
	cases := map[string]bool{
		"http://localhost": true, "http://localhost:3000": true, "https://localhost": true, "http://127.0.0.1:20128": true, "http://[::1]:20128": true,
		"HTTP://LOCALHOST:3000": true, "http://localhost.": true,
		"null": false, "": false, "https://evil.com": false, "http://localhost.evil.com": false, "http://evil.com:3000": false,
		"file://": false, "chrome-extension://abcdef": false, "http://user@localhost": false, "http://localhost@evil.com": false,
		"localhost": false, "//localhost": false, "http://": false, "http://0.0.0.0": false, "ftp://localhost": false, "http://localhost/path": false,
		"http://localhost:3000/": false, "javascript:alert(1)": false, "http://[::1": false,
	}
	for origin, want := range cases {
		if got := loopbackOrigin(origin); got != want {
			t.Errorf("loopbackOrigin(%q) = %v, want %v", origin, got, want)
		}
	}
}

func TestIsLocalRequest(t *testing.T) {
	type c struct {
		name  string
		r     *nethttp.Request
		trust bool
		want  bool
	}
	good := func() map[string]string { return map[string]string{} }
	with := func(k, v string) map[string]string { h := good(); h[k] = v; return h }
	cases := []c{
		{"plain loopback", req("127.0.0.1:5000", "localhost:20128", good()), true, true},
		{"ipv6 loopback", req("[::1]:5000", "[::1]:20128", good()), true, true},
		{"loopback with loopback origin", req("127.0.0.1:5000", "localhost:20128", with("Origin", "http://localhost:3000")), true, true},
		{"same-origin fetch", req("127.0.0.1:5000", "localhost:20128", with("Sec-Fetch-Site", "same-origin")), true, true},
		{"navigation", req("127.0.0.1:5000", "localhost:20128", with("Sec-Fetch-Site", "none")), true, true},
		{"remote peer", req("203.0.113.9:5000", "localhost:20128", good()), true, false},
		{"remote peer with loopback host", req("203.0.113.9:5000", "127.0.0.1", good()), true, false},
		{"trust off", req("127.0.0.1:5000", "localhost:20128", good()), false, false},
		{"forwarded for", req("127.0.0.1:5000", "localhost", with("X-Forwarded-For", "203.0.113.9")), true, false},
		{"forwarded for loopback", req("127.0.0.1:5000", "localhost", with("X-Forwarded-For", "127.0.0.1")), true, false},
		{"real ip", req("127.0.0.1:5000", "localhost", with("X-Real-Ip", "203.0.113.9")), true, false},
		{"forwarded", req("127.0.0.1:5000", "localhost", with("Forwarded", "for=203.0.113.9")), true, false},
		{"forwarded host", req("127.0.0.1:5000", "localhost", with("X-Forwarded-Host", "evil.com")), true, false},
		{"forwarded proto", req("127.0.0.1:5000", "localhost", with("X-Forwarded-Proto", "https")), true, false},
		{"via", req("127.0.0.1:5000", "localhost", with("Via", "1.1 proxy")), true, false},
		{"cdn connecting ip", req("127.0.0.1:5000", "localhost", with("Cf-Connecting-Ip", "203.0.113.9")), true, false},
		{"true client ip", req("127.0.0.1:5000", "localhost", with("True-Client-Ip", "203.0.113.9")), true, false},
		{"dns rebinding host", req("127.0.0.1:5000", "evil.com:20128", good()), true, false},
		{"empty host", req("127.0.0.1:5000", "", good()), true, false},
		{"wildcard host", req("127.0.0.1:5000", "0.0.0.0:20128", good()), true, false},
		{"foreign origin", req("127.0.0.1:5000", "localhost", with("Origin", "https://evil.com")), true, false},
		{"null origin", req("127.0.0.1:5000", "localhost", with("Origin", "null")), true, false},
		{"cross-site", req("127.0.0.1:5000", "localhost", with("Sec-Fetch-Site", "cross-site")), true, false},
		{"same-site other port", req("127.0.0.1:5000", "localhost:20128", with("Sec-Fetch-Site", "same-site")), true, false},
		{"unknown fetch site", req("127.0.0.1:5000", "localhost", with("Sec-Fetch-Site", "weird")), true, false},
		{"fetch site case", req("127.0.0.1:5000", "localhost", with("Sec-Fetch-Site", "Same-Origin")), true, true},
	}
	for _, tc := range cases {
		if got := isLocalRequest(tc.r, tc.trust); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestEmptyForwardedHeadersStillCountAsPresent(t *testing.T) {
	r := req("127.0.0.1:5000", "localhost", nil)
	r.Header["X-Forwarded-For"] = []string{""}
	if isLocalRequest(r, true) {
		t.Fatal("an empty X-Forwarded-For hid a proxy hop")
	}
	r = req("127.0.0.1:5000", "localhost", nil)
	r.Header["Forwarded"] = nil
	if isLocalRequest(r, true) {
		t.Fatal("a Forwarded key with no value hid a proxy hop")
	}
}

func TestMultipleOriginHeadersAreNotLocal(t *testing.T) {
	r := req("127.0.0.1:5000", "localhost", nil)
	r.Header["Origin"] = []string{"http://localhost", "https://evil.com"}
	if isLocalRequest(r, true) {
		t.Fatal("two Origin headers were accepted")
	}
	r = req("127.0.0.1:5000", "localhost", nil)
	r.Header["Sec-Fetch-Site"] = []string{"same-origin", "cross-site"}
	if isLocalRequest(r, true) {
		t.Fatal("two Sec-Fetch-Site headers were accepted")
	}
}

func TestSplitHost(t *testing.T) {
	type r struct {
		host string
		ok   bool
	}
	cases := map[string]r{
		"localhost":         {"localhost", true},
		"localhost:20128":   {"localhost", true},
		"[::1]":             {"::1", true},
		"[::1]:20128":       {"::1", true},
		"::1":               {"::1", true},
		"":                  {"", false},
		"user@localhost":    {"", false},
		"localhost/path":    {"", false},
		"local\\host":       {"", false},
		"local host":        {"", false},
		"local\thost":       {"", false},
		"::1%lo":            {"", false},
		"localhost%00":      {"", false},
		"[::1":              {"", false},
		"[::1]x":            {"", false},
		"[::1]:":            {"", false},
		"[::1]:abc":         {"", false},
		"localhost:":        {"", false},
		"localhost:abc":     {"", false},
		"localhost:1234567": {"", false},
	}
	for in, want := range cases {
		h, ok := splitHost(in)
		if ok != want.ok || (ok && h != want.host) {
			t.Errorf("splitHost(%q) = %q, %v; want %q, %v", in, h, ok, want.host, want.ok)
		}
	}
}

func TestZonedLoopbackPeerIsNotLocal(t *testing.T) {
	for _, remote := range []string{"[::1%lo]:80", "[::1%eth0]:5000", "[fe80::1%lo]:80"} {
		if loopbackPeer(remote) {
			t.Errorf("%s was accepted", remote)
		}
	}
	if !loopbackPeer("[::ffff:127.0.0.1]:80") || loopbackPeer("[::ffff:10.0.0.1]:80") {
		t.Error("IPv4-mapped addresses must be judged by the embedded IPv4 address")
	}
}

func TestDuplicatedFetchSiteOrOriginIsNeverTrusted(t *testing.T) {
	for _, order := range [][]string{{"same-origin", "cross-site"}, {"cross-site", "same-origin"}, {"none", "same-site"}, {"same-site", "none"}, {"same-origin", "same-origin"}} {
		r := req("127.0.0.1:5000", "localhost", nil)
		r.Header["Sec-Fetch-Site"] = order
		if isLocalRequest(r, true) {
			t.Errorf("Sec-Fetch-Site %v was accepted", order)
		}
	}
	for _, order := range [][]string{{"https://evil.com", "http://localhost"}, {"http://localhost", "http://localhost"}} {
		r := req("127.0.0.1:5000", "localhost", nil)
		r.Header["Origin"] = order
		if isLocalRequest(r, true) {
			t.Errorf("Origin %v was accepted", order)
		}
	}
}

func TestOriginHostStripsTheSchemeAndTheSpacesAroundIt(t *testing.T) {
	for in, want := range map[string]string{
		"http://localhost:3000": "localhost:3000", "HTTPS://Example.COM": "example.com",
		"  http://a.test  ": "a.test", "\thttps://b.test\n": "b.test",
	} {
		if got, ok := OriginHost(in); !ok || got != want {
			t.Errorf("%q: %q %v, want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "null", "ftp://x", "localhost", "  null  "} {
		if got, ok := OriginHost(in); ok {
			t.Errorf("%q gave %q", in, got)
		}
	}
}
