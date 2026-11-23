package providers

import (
	"context"
	"testing"

	apphttp "rllmx/app/http"
	"rllmx/app/platform/settings"
	"rllmx/app/store/repository"
	"rllmx/tests"
	"rllmx/tests/store"
)

// The password form is hidden only when a single sign-on can finish. The row is
// stored through the repository, the way both the settings API and auth:mode
// write it, and the authenticator reads it back, so this pins the wiring the
// unit tests of oidc and saml cannot see.
func TestAnUnusableSSOKeepsThePasswordForm(t *testing.T) {
	cases := map[string]struct {
		row  map[string]string
		want struct{ password, oidc bool }
	}{
		"oidc complete": {
			map[string]string{"authMode": `"oidc"`, "oidcIssuerUrl": `"https://idp.example.com"`, "oidcClientId": `"cid"`, "oidcClientSecret": `"s3cret"`},
			struct{ password, oidc bool }{false, true},
		},
		"oidc issuer with a path": {
			map[string]string{"authMode": `"oidc"`, "oidcIssuerUrl": `"https://idp.example.com/tenant"`, "oidcClientId": `"cid"`, "oidcClientSecret": `"s3cret"`},
			struct{ password, oidc bool }{true, false},
		},
		"oidc without a secret": {
			map[string]string{"authMode": `"oidc"`, "oidcIssuerUrl": `"https://idp.example.com"`, "oidcClientId": `"cid"`},
			struct{ password, oidc bool }{true, false},
		},
	}
	for name, c := range cases {
		s := store.Migrated(t)
		if err := s.Store.Settings().Patch(context.Background(), c.row); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		a := newAuthenticator(&fakeApp{}, tests.Config{Values: map[string]any{"auth.api_key_secret": "k"}},
			func() *repository.Store { return s.Store }, func() []byte { return []byte("0123456789abcdef0123456789abcdef") },
			&settings.Server{TrustLoopback: true}, apphttp.NewTrust(nil))
		got := a.Mode(context.Background())
		if got.Password != c.want.password || got.OIDC != c.want.oidc {
			t.Errorf("%s: password %t oidc %t, want password %t oidc %t", name, got.Password, got.OIDC, c.want.password, c.want.oidc)
		}
	}
}
