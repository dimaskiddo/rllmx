package saml

import (
	"encoding/base64"
	"encoding/pem"
	"strings"
	"testing"

	"rllmx/app/store/document"
)

func TestSettingsFromDoc(t *testing.T) {
	d, err := document.ParseDoc(`{"samlEntryPoint":"https://idp.example.com/sso","samlIssuer":"","samlCert":"CERT","samlLoginLabel":"Sign in with SSO","samlAttributeEmail":"mail","samlAttributeName":"cn","samlIdpEntityId":"https://idp.example.com/metadata"}`)
	if err != nil {
		t.Fatal(err)
	}
	s := FromDoc(d)
	if s.EntryPoint != "https://idp.example.com/sso" || s.Cert != "CERT" || s.LoginLabel != "Sign in with SSO" {
		t.Fatalf("%+v", s)
	}
	if s.AttributeEmail != "mail" || s.AttributeName != "cn" || s.IDPEntityID != "https://idp.example.com/metadata" {
		t.Fatalf("%+v", s)
	}
	if s.EntityID != "" {
		t.Fatalf("the entity id override defaults to %q, want empty", s.EntityID)
	}
}

func TestConfiguredNeedsAnEntryPointAndACertificate(t *testing.T) {
	full := Settings{EntryPoint: "https://idp.example.com/sso", Cert: "CERT"}
	if !full.Configured() {
		t.Fatal("a complete pair is not configured")
	}
	for name, mutate := range map[string]func(*Settings){
		"no entry point": func(s *Settings) { s.EntryPoint = "" },
		"no cert":        func(s *Settings) { s.Cert = "" },
	} {
		copy := full
		mutate(&copy)
		if copy.Configured() {
			t.Errorf("%s: still configured", name)
		}
	}
	if (Settings{}).Configured() {
		t.Fatal("an empty settings value is configured")
	}
}

func TestTheEntityIDDefaultsToTheMetadataURL(t *testing.T) {
	if got := (Settings{}).EntityIDFor("https://gw.example.com"); got != "https://gw.example.com/api/auth/saml/metadata" {
		t.Fatalf("%q", got)
	}
	if got := (Settings{}).EntityIDFor("https://gw.example.com/"); got != "https://gw.example.com/api/auth/saml/metadata" {
		t.Fatalf("%q", got)
	}
	override := Settings{EntityID: "urn:9router:sp"}
	if got := override.EntityIDFor("https://gw.example.com"); got != "urn:9router:sp" {
		t.Fatalf("%q: the override did not win", got)
	}
}

func TestValidate(t *testing.T) {
	p := newTestIDP(t)
	pemCert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: p.cert.Raw})
	bare := base64.StdEncoding.EncodeToString(p.cert.Raw)

	good := Settings{EntryPoint: "https://idp.example.com/sso", Cert: string(pemCert)}
	if _, err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	bareCert := Settings{EntryPoint: "http://127.0.0.1:8080/sso", Cert: bare}
	if _, err := bareCert.Validate(); err != nil {
		t.Fatalf("a bare base64 certificate or a loopback entry point was refused: %v", err)
	}

	for name, tc := range map[string]Settings{
		"plain http":           {EntryPoint: "http://idp.example.com/sso", Cert: string(pemCert)},
		"no entry point":       {Cert: string(pemCert)},
		"credentials in entry": {EntryPoint: "https://u:p@idp.example.com/sso", Cert: string(pemCert)},
		"no certificate":       {EntryPoint: "https://idp.example.com/sso"},
		"not a certificate":    {EntryPoint: "https://idp.example.com/sso", Cert: "not a certificate"},
		"bad entity id":        {EntryPoint: "https://idp.example.com/sso", Cert: string(pemCert), EntityID: "example.com/sp"},
		"entity id with user":  {EntryPoint: "https://idp.example.com/sso", Cert: string(pemCert), EntityID: "https://u:p@example.com/sp"},
	} {
		if _, err := tc.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestTheEntityIDOverrideAcceptsBothSpellings(t *testing.T) {
	p := newTestIDP(t)
	pemCert := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: p.cert.Raw}))
	for _, id := range []string{"urn:9router:sp", "urn:example:sp", "https://gw.example.com/api/auth/saml/metadata"} {
		tc := Settings{EntryPoint: "https://idp.example.com/sso", Cert: pemCert, EntityID: id}
		if _, err := tc.Validate(); err != nil {
			t.Errorf("%q refused: %v", id, err)
		}
	}
}

func TestErrorsNameNoValue(t *testing.T) {
	for _, err := range []error{ErrConfig, ErrParse, ErrSignature, ErrReplay} {
		if strings.Contains(err.Error(), "example.com") {
			t.Errorf("%v names a value", err)
		}
	}
	if _, err := ParseCertificate("secret-looking-value"); err == nil || strings.Contains(err.Error(), "secret-looking") {
		t.Fatalf("err %v", err)
	}
}

func TestAChangedCertificateTextIsParsedAgainAndAnUnchangedOneIsReused(t *testing.T) {
	a, b := newTestIDP(t), newTestIDP(t)
	textA := base64.StdEncoding.EncodeToString(a.cert.Raw)
	textB := base64.StdEncoding.EncodeToString(b.cert.Raw)

	first, err := ParseCertificate(textA)
	if err != nil {
		t.Fatal(err)
	}
	again, err := ParseCertificate("  " + textA + "\n")
	if err != nil || again != first {
		t.Fatalf("an unchanged value was parsed again: same=%v err=%v", again == first, err)
	}
	other, err := ParseCertificate(textB)
	if err != nil {
		t.Fatal(err)
	}
	if other == first || !other.Equal(b.cert) {
		t.Fatal("a changed value kept the old certificate")
	}
	back, err := ParseCertificate(textA)
	if err != nil || !back.Equal(a.cert) {
		t.Fatalf("going back to the first value gave the wrong certificate: %v", err)
	}
}
