package http

import (
	nethttp "net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func trustWith(t *testing.T, configured ...string) Trust {
	t.Helper()
	var prefixes []netip.Prefix
	for _, c := range configured {
		prefixes = append(prefixes, netip.MustParsePrefix(c))
	}
	return NewTrust(prefixes)
}

func from(remote string, hdr map[string]string) *nethttp.Request {
	r := httptest.NewRequest(nethttp.MethodGet, "/api/addr", nil)
	r.RemoteAddr = remote
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	return r
}

func TestClientAddrIgnoresForwardedHeadersFromUntrustedPeers(t *testing.T) {
	tr := trustWith(t)
	for _, h := range []map[string]string{
		{"X-Forwarded-For": "198.51.100.7"},
		{"X-Real-Ip": "198.51.100.7"},
		{"Forwarded": "for=198.51.100.7"},
		{"X-Forwarded-For": "198.51.100.7, 10.0.0.1"},
	} {
		if got := tr.ClientAddr(from("203.0.113.9:5000", h)); got != "203.0.113.9" {
			t.Errorf("%v: %q", h, got)
		}
	}
}

func TestClientAddrFollowsXForwardedForFromALoopbackPeer(t *testing.T) {
	tr := trustWith(t)
	if got := tr.ClientAddr(from("127.0.0.1:5000", map[string]string{"X-Forwarded-For": "198.51.100.7"})); got != "198.51.100.7" {
		t.Fatalf("%q", got)
	}
}

func TestClientAddrIgnoresXRealIPEvenFromATrustedPeer(t *testing.T) {
	tr := trustWith(t, "10.0.0.0/8")
	if got := tr.ClientAddr(from("10.1.2.3:5000", map[string]string{"X-Real-Ip": "198.51.100.7"})); got != "10.1.2.3" {
		t.Fatalf("%q: X-Real-Ip is client-settable behind a proxy that only sets X-Forwarded-For", got)
	}
}

func TestClientAddrFollowsAConfiguredProxy(t *testing.T) {
	tr := trustWith(t, "10.0.0.0/8", "192.0.2.1/32")
	cases := []struct {
		remote string
		hdr    map[string]string
		want   string
	}{
		{"10.1.2.3:5000", map[string]string{"X-Forwarded-For": "198.51.100.7"}, "198.51.100.7"},
		{"192.0.2.1:5000", map[string]string{"X-Forwarded-For": "198.51.100.7"}, "198.51.100.7"},
		{"192.0.2.2:5000", map[string]string{"X-Forwarded-For": "198.51.100.7"}, "192.0.2.2"},
		{"10.1.2.3:5000", map[string]string{"X-Forwarded-For": "198.51.100.7, 10.9.9.9"}, "198.51.100.7"},
		{"10.1.2.3:5000", map[string]string{"X-Forwarded-For": "1.1.1.1, 198.51.100.7"}, "198.51.100.7"},
		{"10.1.2.3:5000", map[string]string{"X-Forwarded-For": "garbage"}, "10.1.2.3"},
		{"10.1.2.3:5000", nil, "10.1.2.3"},
		{"[::ffff:10.1.2.3]:5000", map[string]string{"X-Forwarded-For": "198.51.100.7"}, "198.51.100.7"},
	}
	for _, c := range cases {
		if got := tr.ClientAddr(from(c.remote, c.hdr)); got != c.want {
			t.Errorf("%s %v: %q, want %q", c.remote, c.hdr, got, c.want)
		}
	}
}

func TestClientAddrOfAnUnparsablePeerIsTheRawValue(t *testing.T) {
	if got := trustWith(t).ClientAddr(from("not-an-address", nil)); got != "not-an-address" {
		t.Fatalf("%q", got)
	}
}

func TestSecureRequest(t *testing.T) {
	tr := trustWith(t, "10.0.0.0/8")
	cases := []struct {
		name   string
		remote string
		hdr    map[string]string
		want   bool
	}{
		{"plain", "203.0.113.9:5000", nil, false},
		{"untrusted peer claims https", "203.0.113.9:5000", map[string]string{"X-Forwarded-Proto": "https"}, false},
		{"loopback proxy says https", "127.0.0.1:5000", map[string]string{"X-Forwarded-Proto": "https"}, true},
		{"configured proxy says https", "10.1.2.3:5000", map[string]string{"X-Forwarded-Proto": "https"}, true},
		{"configured proxy says HTTPS", "10.1.2.3:5000", map[string]string{"X-Forwarded-Proto": "HTTPS"}, true},
		{"configured proxy says http", "10.1.2.3:5000", map[string]string{"X-Forwarded-Proto": "http"}, false},
		{"configured proxy says nothing", "10.1.2.3:5000", nil, false},
		{"last hop http wins", "10.1.2.3:5000", map[string]string{"X-Forwarded-Proto": "https, http"}, false},
		{"last hop https wins", "10.1.2.3:5000", map[string]string{"X-Forwarded-Proto": "http, https"}, true},
		{"garbage", "10.1.2.3:5000", map[string]string{"X-Forwarded-Proto": "httpsx"}, false},
	}
	for _, c := range cases {
		if got := tr.SecureRequest(from(c.remote, c.hdr)); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestSecureRequestTrustsTLS(t *testing.T) {
	tr := trustWith(t)
	if !tr.SecureRequest(httptest.NewRequest(nethttp.MethodGet, "https://gw.example.com/", nil)) {
		t.Fatal("a TLS request must be secure")
	}
	r := httptest.NewRequest(nethttp.MethodGet, "http://gw.example.com/", nil)
	r.RemoteAddr = "not-an-address"
	r.Header.Set("X-Forwarded-Proto", "https")
	if tr.SecureRequest(r) {
		t.Fatal("an unparsable peer must not be trusted")
	}
}

func TestSecureRequestSeesThroughAnIPv4MappedPeer(t *testing.T) {
	tr := trustWith(t, "10.0.0.0/8")
	r := from("[::ffff:10.1.2.3]:5000", map[string]string{"X-Forwarded-Proto": "https"})
	if !tr.SecureRequest(r) {
		t.Fatal("a dual-stack listener reports IPv4 proxies in mapped form")
	}
	r.RemoteAddr = "[::ffff:203.0.113.9]:5000"
	if tr.SecureRequest(r) {
		t.Fatal("a mapped address outside the list was trusted")
	}
}

func TestSecureRequestNeedsAnAddressWithAPort(t *testing.T) {
	tr := trustWith(t, "10.0.0.0/8")
	for _, addr := range []string{"10.1.2.3", "", "10.1.2.3:", ":5000"} {
		if tr.SecureRequest(from(addr, map[string]string{"X-Forwarded-Proto": "https"})) {
			t.Errorf("%q was trusted", addr)
		}
	}
}
