package ucweb

import (
	"net/url"
	"testing"
)

func FuzzClaim(f *testing.F) {
	f.Add("x.eyJzaWQiOiJzIn0.y")
	f.Add("not.a.token")
	f.Fuzz(func(t *testing.T, token string) {
		_ = claim(token, "uid")
	})
}

func FuzzPublic(f *testing.F) {
	f.Add("https://cdn.example/a.png")
	f.Add("http://127.0.0.1/a")
	f.Fuzz(func(t *testing.T, raw string) {
		if err := public(raw); err != nil {
			return
		}
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
			t.Fatalf("public accepted %q", raw)
		}
	})
}
