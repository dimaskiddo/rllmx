package oidc

import "testing"

// Usable decides whether the password form may be hidden. An issuer that
// discovery will refuse must never count, or the dashboard locks its owner out.
func TestUsableNeedsAnIssuerDiscoveryAccepts(t *testing.T) {
	good := Settings{Issuer: "https://idp.example.com", ClientID: "cid", ClientSecret: "s"}
	cases := map[string]struct {
		mutate func(*Settings)
		want   bool
	}{
		"complete":            {func(*Settings) {}, true},
		"loopback http":       {func(s *Settings) { s.Issuer = "http://localhost:8080" }, true},
		"no secret":           {func(s *Settings) { s.ClientSecret = "" }, false},
		"no client id":        {func(s *Settings) { s.ClientID = "" }, false},
		"no issuer":           {func(s *Settings) { s.Issuer = "" }, false},
		"issuer with a path":  {func(s *Settings) { s.Issuer = "https://idp.example.com/tenant" }, false},
		"issuer with a query": {func(s *Settings) { s.Issuer = "https://idp.example.com?x=1" }, false},
		"plain http remote":   {func(s *Settings) { s.Issuer = "http://idp.example.com" }, false},
		"nine scopes":         {func(s *Settings) { s.Scopes = "a b c d e f g h i" }, false},
	}
	for name, c := range cases {
		s := good
		c.mutate(&s)
		if got := s.Usable(); got != c.want {
			t.Errorf("%s: Usable() = %t, want %t", name, got, c.want)
		}
	}
}
