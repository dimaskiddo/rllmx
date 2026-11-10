package network

import (
	"errors"
	"net/netip"
	"testing"
)

func addr(t *testing.T, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestClassify(t *testing.T) {
	cases := []struct {
		ip   string
		want Class
	}{
		{"8.8.8.8", Public},
		{"1.1.1.1", Public},
		{"2001:4860:4860::8888", Public},
		{"127.0.0.1", Internal},
		{"127.9.9.9", Internal},
		{"::1", Internal},
		{"10.0.0.1", Internal},
		{"172.16.0.1", Internal},
		{"172.31.255.255", Internal},
		{"192.168.1.1", Internal},
		{"100.64.0.1", Internal},
		{"0.0.0.0", Internal},
		{"fc00::1", Internal},
		{"ff02::1", Internal},
		{"255.255.255.255", Internal},
		{"169.254.0.1", Never},
		{"169.254.169.254", Never},
		{"fe80::1", Never},
		{"fd00:ec2::254", Never},
		{"::ffff:10.0.0.1", Internal},
		{"::ffff:127.0.0.1", Internal},
		{"::ffff:8.8.8.8", Public},
		{"64:ff9b::a00:1", Internal},
		{"64:ff9b::808:808", Public},
		{"::a9fe:a9fe", Never},
		{"::10.0.0.1", Internal},
		{"::8.8.8.8", Public},
		{"64:ff9b:1::1", Internal},
	}
	for _, tc := range cases {
		if got := Classify(addr(t, tc.ip)); got != tc.want {
			t.Errorf("%s = %v, want %v", tc.ip, got, tc.want)
		}
	}
	if Classify(netip.Addr{}) != Never {
		t.Error("an invalid address is not Never")
	}
}

func TestNeverBeatsTheAllowList(t *testing.T) {
	allow, err := ParseAllow("169.254.0.0/16,fe80::/10,fd00:ec2::254,169.254.169.254")
	if err != nil {
		t.Fatal(err)
	}
	for _, ip := range []string{"169.254.169.254", "169.254.0.1", "fe80::1", "fd00:ec2::254"} {
		if !allow.PermitsAddr(addr(t, ip)) {
			t.Errorf("%s is not on the list", ip)
		}
		if got := permitted(addr(t, ip), allow); got {
			t.Errorf("%s is permitted, but Never must win over a list entry", ip)
		}
	}
}

func TestInternalNeedsAListEntry(t *testing.T) {
	allow, err := ParseAllow("10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	if !permitted(addr(t, "10.1.2.3"), allow) {
		t.Fatal("a listed private address was refused")
	}
	if permitted(addr(t, "192.168.1.1"), allow) {
		t.Fatal("an unlisted private address was permitted")
	}
	if !permitted(addr(t, "8.8.8.8"), allow) {
		t.Fatal("a public address was refused")
	}
	var none *Allow
	if permitted(addr(t, "10.0.0.1"), none) {
		t.Fatal("a nil list permitted a private address")
	}
}

func TestParseAllowEntries(t *testing.T) {
	allow, err := ParseAllow(" idp.example.com , 10.0.0.0/8 , 192.168.1.5 , LocalHost. ")
	if err != nil {
		t.Fatal(err)
	}
	if allow.Len() != 4 {
		t.Fatalf("len %d", allow.Len())
	}
	for _, host := range []string{"idp.example.com", "IDP.EXAMPLE.COM", "idp.example.com.", "localhost"} {
		if !allow.Permits(host) {
			t.Errorf("%q is not permitted", host)
		}
	}
	if allow.Permits("evil.example.com") {
		t.Error("an unlisted name is permitted")
	}
	for _, ip := range []string{"10.9.9.9", "192.168.1.5"} {
		if !allow.PermitsAddr(addr(t, ip)) {
			t.Errorf("%s is not permitted", ip)
		}
	}
	if allow.PermitsAddr(addr(t, "192.168.1.6")) {
		t.Error("an address beside a listed single address is permitted")
	}
}

func TestParseAllowEmptyAndBad(t *testing.T) {
	for _, raw := range []string{"", "  ", " , , "} {
		allow, err := ParseAllow(raw)
		if err != nil || allow != nil {
			t.Errorf("%q: %v %v", raw, allow, err)
		}
	}
	for _, raw := range []string{"bad host", "10.0.0.0/33", "http://example.com", "a..b", "-bad.example.com"} {
		if _, err := ParseAllow(raw); err == nil {
			t.Errorf("%q accepted", raw)
		}
	}
}

func TestParseAllowErrorsNameNoValue(t *testing.T) {
	_, err := ParseAllow("secret token/with?punctuation")
	if err == nil {
		t.Fatal("a bad entry was accepted")
	}
	if got := err.Error(); contains(got, "secret token") {
		t.Fatalf("the error repeats the value: %q", got)
	}
}

func TestHostPortAndEmbeddedAgreeWithLoopback(t *testing.T) {
	// The two spellings IsLoopbackHost accepts must classify as Internal, so the
	// guard and the locality check cannot disagree (divergence 146).
	for _, spelling := range []string{"localhost", "LOCALHOST", "127.0.0.1", "127.9.9.9", "::1", "::ffff:127.0.0.1"} {
		if !IsLoopbackHost(spelling) {
			continue
		}
		a, err := netip.ParseAddr(spelling)
		if err != nil {
			continue
		}
		if Classify(a) != Internal {
			t.Errorf("%s is loopback but not Internal", spelling)
		}
	}
}

func TestErrBlockedNamesNoAddress(t *testing.T) {
	if got := ErrBlocked.Error(); contains(got, "169.254") || contains(got, ":") && contains(got, "//") {
		t.Fatalf("the error names an address: %q", got)
	}
	if !errors.Is(ErrBlocked, ErrBlocked) {
		t.Fatal("ErrBlocked is not itself")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
