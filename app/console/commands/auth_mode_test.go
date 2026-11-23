package commands

import (
	"context"
	"strings"
	"testing"

	"rllmx/app/store/document"
)

// authModeSettings is the stored row after one command run, with secrets opened.
func authModeSettings(t *testing.T, r *rig) map[string]string {
	t.Helper()
	d, err := r.st.Settings().Effective(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, key := range []string{"authMode", "ssoType", "oidcIssuerUrl", "oidcClientId", "oidcClientSecret"} {
		v, _ := d.GetString(key)
		out[key] = v
	}
	return out
}

func authModeRun(t *testing.T, opts map[string]string, answers ...string) (*rig, *authConsole, error) {
	t.Helper()
	r := newRig(t)
	c := &authConsole{opts: opts, secrets: answers}
	return r, c, (&AuthMode{R: r.runtime()}).Handle(c)
}

func TestAuthModeSetsThePasswordForm(t *testing.T) {
	r, c, err := authModeRun(t, map[string]string{"mode": "password"})
	if err != nil {
		t.Fatal(err)
	}
	if got := authModeSettings(t, r)["authMode"]; got != "password" {
		t.Fatalf("authMode = %q", got)
	}
	if !strings.Contains(c.output(), "sign-in offered: password") {
		t.Fatalf("output %q", c.output())
	}
}

func TestAuthModeConfiguresOIDCInOneStep(t *testing.T) {
	r, c, err := authModeRun(t, map[string]string{
		"mode": "oidc", "issuer": "https://idp.example.com", "client-id": "cid",
	}, "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	got := authModeSettings(t, r)
	if got["authMode"] != "oidc" || got["oidcIssuerUrl"] != "https://idp.example.com" || got["oidcClientId"] != "cid" || got["oidcClientSecret"] != "s3cret" {
		t.Fatalf("settings = %v", got)
	}
	if !strings.Contains(c.output(), "sign-in offered: OIDC") {
		t.Fatalf("output %q", c.output())
	}
	var raw []string
	if err := r.st.SQ.DB.Select(&raw, `SELECT "data" FROM "settings" WHERE "id" = 1`); err != nil {
		t.Fatal(err)
	}
	if len(raw) != 1 || strings.Contains(raw[0], "s3cret") {
		t.Fatal("the client secret is stored in plain text")
	}
}

func TestAuthModeKeepsAStoredSecretAndAsksForNone(t *testing.T) {
	r := newRig(t)
	first := &authConsole{opts: map[string]string{"mode": "oidc", "issuer": "https://idp.example.com", "client-id": "cid"}, secrets: []string{"s3cret"}}
	if err := (&AuthMode{R: r.runtime()}).Handle(first); err != nil {
		t.Fatal(err)
	}
	c := &authConsole{opts: map[string]string{"mode": "oidc"}}
	if err := (&AuthMode{R: r.runtime()}).Handle(c); err != nil {
		t.Fatalf("a stored secret was not reused: %v", err)
	}
	if got := authModeSettings(t, r)["oidcClientSecret"]; got != "s3cret" {
		t.Fatalf("secret = %q", got)
	}
}

// What the command reports must be what the server then offers. Both read the
// same Usable rules, so a row discovery would refuse keeps the password form in
// the report instead of promising a button that cannot finish.
func TestAuthModeReportsWhatTheServerWouldOffer(t *testing.T) {
	for name, c := range map[string]struct {
		row                  string
		password, oidc, saml bool
	}{
		"oidc complete": {
			`{"authMode":"oidc","oidcIssuerUrl":"https://idp.example.com","oidcClientId":"c","oidcClientSecret":"s"}`,
			false, true, false,
		},
		"oidc issuer with a path": {
			`{"authMode":"oidc","oidcIssuerUrl":"https://idp.example.com/t","oidcClientId":"c","oidcClientSecret":"s"}`,
			true, false, false,
		},
		"saml without an idp entity id": {
			`{"authMode":"saml","samlEntryPoint":"https://idp.example.com/sso","samlCert":"x"}`,
			true, false, false,
		},
		"password": {`{"authMode":"password"}`, true, false, false},
	} {
		d, err := document.ParseDoc(c.row)
		if err != nil {
			t.Fatal(err)
		}
		got := resolveMode(d)
		if got.Password != c.password || got.OIDC != c.oidc || got.SAML != c.saml {
			t.Errorf("%s: %+v", name, got)
		}
	}
}

// A secret belongs to one client at one provider. Keeping it after either
// changes would turn the password form off for a login that can never finish.
func TestAuthModeAsksForTheSecretWhenTheClientChanges(t *testing.T) {
	first := map[string]string{"mode": "oidc", "issuer": "https://idp.example.com", "client-id": "cid"}
	for name, change := range map[string]map[string]string{
		"new issuer":    {"mode": "oidc", "issuer": "https://other.example.com"},
		"new client id": {"mode": "oidc", "client-id": "cid2"},
	} {
		r := newRig(t)
		if err := (&AuthMode{R: r.runtime()}).Handle(&authConsole{opts: first, secrets: []string{"old-secret"}}); err != nil {
			t.Fatal(err)
		}
		c := &authConsole{opts: change, secrets: []string{"new-secret"}}
		if err := (&AuthMode{R: r.runtime()}).Handle(c); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(c.secrets) != 0 {
			t.Errorf("%s: the stored secret was reused for a different client", name)
		}
		if got := authModeSettings(t, r)["oidcClientSecret"]; got != "new-secret" {
			t.Errorf("%s: secret = %q", name, got)
		}
	}
}

func TestAuthModeSameClientAgainKeepsTheStoredSecret(t *testing.T) {
	r := newRig(t)
	opts := map[string]string{"mode": "oidc", "issuer": "https://idp.example.com", "client-id": "cid"}
	if err := (&AuthMode{R: r.runtime()}).Handle(&authConsole{opts: opts, secrets: []string{"s3cret"}}); err != nil {
		t.Fatal(err)
	}
	again := map[string]string{"mode": "oidc", "issuer": "https://idp.example.com/", "client-id": "cid"}
	if err := (&AuthMode{R: r.runtime()}).Handle(&authConsole{opts: again}); err != nil {
		t.Fatalf("the same client asked for its secret again: %v", err)
	}
}

func TestAuthModeBothOffersEveryConfiguredSingleSignOn(t *testing.T) {
	r, c, err := authModeRun(t, map[string]string{
		"mode": "both", "issuer": "https://idp.example.com", "client-id": "cid",
	}, "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	if got := authModeSettings(t, r)["authMode"]; got != "both" {
		t.Fatalf("authMode = %q", got)
	}
	if !strings.Contains(c.output(), "sign-in offered: OIDC") || strings.Contains(c.output(), "offered: password") {
		t.Fatalf("output %q", c.output())
	}
}

func TestAuthModeLeavesThePasswordFormOnWhenTheSSOIsNotConfigured(t *testing.T) {
	_, c, err := authModeRun(t, map[string]string{"mode": "saml"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.output(), "offered: password") {
		t.Fatalf("output %q", c.output())
	}
	if len(c.errors) == 0 || !strings.Contains(strings.Join(c.errors, "\n"), "not configured") {
		t.Fatalf("warnings %v", c.errors)
	}
}

func TestAuthModeAsksForTheMode(t *testing.T) {
	r, _, err := authModeRun(t, map[string]string{}, "password")
	if err != nil {
		t.Fatal(err)
	}
	if got := authModeSettings(t, r)["authMode"]; got != "password" {
		t.Fatalf("authMode = %q", got)
	}
}

func TestAuthModeRefusesWhatTheSettingsAPIWould(t *testing.T) {
	tests := map[string]map[string]string{
		"unknown mode":     {"mode": "magic"},
		"bad sso type":     {"mode": "sso", "sso-type": "oauth"},
		"oidc unset":       {"mode": "oidc"},
		"issuer not https": {"mode": "oidc", "issuer": "http://idp.example.com", "client-id": "cid"},
	}
	for name, opts := range tests {
		t.Run(name, func(t *testing.T) {
			r, _, err := authModeRun(t, opts)
			if err == nil {
				t.Fatal("the value was accepted")
			}
			d, err := r.st.Settings().Get(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := d.GetRaw("authMode"); ok {
				t.Fatal("a refused run wrote the mode")
			}
		})
	}
}

func TestAuthModeWithoutATerminalCannotAskForTheSecret(t *testing.T) {
	r := newRig(t)
	r.tty = false
	c := &authConsole{opts: map[string]string{
		"mode": "oidc", "issuer": "https://idp.example.com", "client-id": "cid",
	}}
	err := (&AuthMode{R: r.runtime()}).Handle(c)
	if err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Fatalf("err = %v", err)
	}
	d, err := r.st.Settings().Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := d.GetRaw("authMode"); ok {
		t.Fatal("a refused run wrote the mode")
	}
}

func TestAuthModeDeclaresItsFlags(t *testing.T) {
	got := map[string]bool{}
	for _, f := range (&AuthMode{}).Extend().Flags {
		got[flagName(f)] = true
	}
	for _, want := range []string{"mode", "issuer", "client-id", "sso-type"} {
		if !got[want] {
			t.Errorf("%s is not declared: %v", want, got)
		}
	}
	if (&AuthMode{}).Signature() != "auth:mode" {
		t.Fatalf("signature %q", (&AuthMode{}).Signature())
	}
}
