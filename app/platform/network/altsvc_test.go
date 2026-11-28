package network

import (
	"testing"
	"time"
)

func TestParseAltSvc(t *testing.T) {
	const day = 24 * time.Hour
	tests := []struct {
		name  string
		value string
		port  string
		ttl   time.Duration
		clear bool
		ok    bool
	}{
		{"plain", `h3=":443"; ma=3600`, "443", time.Hour, false, true},
		{"default max age", `h3=":443"`, "443", day, false, true},
		{"capped", `h3=":443"; ma=999999`, "443", day, false, true},
		{"overflow capped", `h3=":443"; ma=99999999999999999999999`, "443", day, false, true},
		{"persist ignored", `h3=":443"; ma=60; persist=1`, "443", time.Minute, false, true},
		{"upper case param", `h3=":443"; MA=60`, "443", time.Minute, false, true},
		{"clear", `clear`, "443", 0, true, true},
		{"clear upper", ` CLEAR `, "443", 0, true, true},
		{"zero max age clears", `h3=":443"; ma=0`, "443", 0, true, true},
		{"second entry", `h2=":443", h3=":443"; ma=120`, "443", 2 * time.Minute, false, true},
		{"draft version", `h3-29=":443"; ma=60`, "443", 0, false, false},
		{"other host", `h3="other.example:443"; ma=60`, "443", 0, false, false},
		{"other port", `h3=":8443"; ma=60`, "443", 0, false, false},
		{"own port", `h3=":8443"; ma=60`, "8443", time.Minute, false, true},
		{"not a number", `h3=":443"; ma=soon`, "443", 0, false, false},
		{"negative", `h3=":443"; ma=-5`, "443", 0, false, false},
		{"unquoted", `h3=:443; ma=60`, "443", 0, false, false},
		{"comma in quotes", `h3="a,b:443"; ma=60, h3=":443"; ma=30`, "443", 30 * time.Second, false, true},
		{"garbage", `"""=;;,,`, "443", 0, false, false},
		{"empty", ``, "443", 0, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ttl, clear, ok := parseAltSvc(tc.value, tc.port)
			if ttl != tc.ttl || clear != tc.clear || ok != tc.ok {
				t.Fatalf("got (%v, %v, %v), want (%v, %v, %v)", ttl, clear, ok, tc.ttl, tc.clear, tc.ok)
			}
		})
	}
}

func FuzzParseAltSvc(f *testing.F) {
	for _, s := range []string{`h3=":443"; ma=3600`, `clear`, `h3="x:1", h3=":443"`, `"`, `h3=`, `;;;`} {
		f.Add(s, "443")
	}
	f.Fuzz(func(t *testing.T, value, port string) {
		ttl, clear, ok := parseAltSvc(value, port)
		if ok && !clear && (ttl <= 0 || ttl > 24*time.Hour) {
			t.Fatalf("ttl %v for %q", ttl, value)
		}
		if !ok && (clear || ttl != 0) {
			t.Fatalf("not ok but (%v, %v) for %q", ttl, clear, value)
		}
	})
}
