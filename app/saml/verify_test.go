package saml

import (
	"errors"
	"testing"
	"time"
)

func TestVerifyAcceptsASignedAssertion(t *testing.T) {
	p := newTestIDP(t)
	exp := p.expectFor(NewReplay())
	got, err := Verify(p.build(t, defaultMinted(exp)), exp)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "assert-1" {
		t.Fatalf("assertion %q", got.ID)
	}
}

func TestVerifyAcceptsAResponseSignedAsAWhole(t *testing.T) {
	p := newTestIDP(t)
	exp := p.expectFor(NewReplay())
	m := defaultMinted(exp)
	m.signAssertionOnly = false
	m.advice = false
	// Sign the Response: render, then sign the root.
	if _, err := Verify(p.buildSignedResponse(t, m), exp); err != nil {
		t.Fatal(err)
	}
}

func TestAnUnsignedResponseIsRefused(t *testing.T) {
	p := newTestIDP(t)
	exp := p.expectFor(NewReplay())
	m := defaultMinted(exp)
	m.unsigned = true
	if _, err := Verify(p.build(t, m), exp); !errors.Is(err, ErrSignature) {
		t.Fatalf("err %v", err)
	}
}

func TestASignatureFromAnotherKeyIsRefused(t *testing.T) {
	other := newTestIDP(t)
	p := newTestIDP(t)
	exp := p.expectFor(NewReplay())
	if _, err := Verify(other.build(t, defaultMinted(exp)), exp); !errors.Is(err, ErrSignature) {
		t.Fatalf("err %v", err)
	}
}

func TestTheWrappingAttackYieldsNoSession(t *testing.T) {
	// The signed assertion travels inside Advice; a forged one is the sibling
	// every consumer reads. Only the verified assertion may supply the address.
	p := newTestIDP(t)
	exp := p.expectFor(NewReplay())
	m := defaultMinted(exp)
	m.advice = true

	got, err := Verify(p.build(t, m), exp)
	if err != nil {
		// Refusing the whole document is a fine answer.
		return
	}
	// Accepting it is fine too, as long as the identity is the one that was
	// signed. The forged sibling must never supply an address.
	if got.ID != "assert-1" {
		t.Fatalf("the wrapped assertion %q was read instead of the signed one", got.ID)
	}
	email, _, ok := Email(got, Settings{})
	if !ok || email == "attacker@evil.example" {
		t.Fatalf("the forged address became the identity: %q", email)
	}
}

func TestASecondUnsignedAssertionIsRefused(t *testing.T) {
	p := newTestIDP(t)
	exp := p.expectFor(NewReplay())
	m := defaultMinted(exp)
	forged := m.forgedAssertionXML()
	m.extraAssertions = []string{forged}

	if _, err := Verify(p.build(t, m), exp); err == nil {
		t.Fatal("two assertions with one signature were accepted")
	}
}

func TestTheClaimRules(t *testing.T) {
	p := newTestIDP(t)
	cases := []struct {
		name   string
		mutate func(*minted)
		want   error
	}{
		{"another issuer", func(m *minted) { m.issuer = "https://evil.example.com" }, ErrWrongIssuer},
		{"another audience", func(m *minted) {
			m.audience = "https://other.example.com/api/auth/saml/metadata"
		}, ErrNoAudience},
		{"another destination", func(m *minted) { m.destination = "https://evil.example.com/acs" }, ErrDestination},
		{"another request", func(m *minted) { m.inResponseTo = "req-other" }, ErrInResponseTo},
		{"expired", func(m *minted) {
			m.notBefore = time.Now().Add(-2 * time.Hour)
			m.notAfter = time.Now().Add(-time.Hour)
		}, ErrExpired},
		{"not yet valid", func(m *minted) {
			m.notBefore = time.Now().Add(time.Hour)
			m.notAfter = time.Now().Add(2 * time.Hour)
		}, ErrNotYetValid},
		{"inside the skew", func(m *minted) { m.notAfter = time.Now().Add(-30 * time.Second) }, nil},
	}
	for _, tc := range cases {
		exp := p.expectFor(NewReplay())
		m := defaultMinted(exp)
		tc.mutate(&m)
		_, err := Verify(p.build(t, m), exp)
		if tc.want == nil && err != nil {
			t.Errorf("%s: %v", tc.name, err)
		}
		if tc.want != nil && !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
	}
}

func TestTheExpectedIssuerIsWhatCounts(t *testing.T) {
	// The comparison must be against the expectation, not against a literal that
	// happens to equal the fixture's issuer: a response is only good for the
	// provider this deployment pinned.
	p := newTestIDP(t)
	exp := p.expectFor(NewReplay())
	body := p.build(t, defaultMinted(exp))

	exp.Issuer = "https://another-idp.example.com"
	if _, err := Verify(body, exp); !errors.Is(err, ErrWrongIssuer) {
		t.Fatalf("a response from an unpinned issuer was accepted: %v", err)
	}
}

func TestAReplayedAssertionIsRefused(t *testing.T) {
	p := newTestIDP(t)
	replay := NewReplay()
	exp := p.expectFor(replay)
	body := p.build(t, defaultMinted(exp))

	if _, err := Verify(body, exp); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(body, exp); !errors.Is(err, ErrReplay) {
		t.Fatalf("the second use: %v", err)
	}
}

func TestADTDIsRefused(t *testing.T) {
	p := newTestIDP(t)
	exp := p.expectFor(NewReplay())
	m := defaultMinted(exp)
	raw := m.render(t, p)
	raw = `<?xml version="1.0"?><!DOCTYPE Response [<!ENTITY x "y">]>` + raw[rawIndex(raw, "<samlp:Response"):]
	encoded := encodeBody(raw)
	if _, err := Verify(encoded, exp); !errors.Is(err, ErrParse) {
		t.Fatalf("err %v", err)
	}
	_ = p
}

func TestAMalformedBodyIsRefused(t *testing.T) {
	p := newTestIDP(t)
	exp := p.expectFor(ReplayAt(time.Now()))
	for _, body := range []string{"", "not base64", encodeBody("<not-saml/>")} {
		if _, err := Verify(body, exp); err == nil {
			t.Errorf("%q accepted", body)
		}
	}
}

func TestVerifyRefusesAnIncompleteExpectation(t *testing.T) {
	p := newTestIDP(t)
	full := p.expectFor(NewReplay())
	body := p.build(t, defaultMinted(full))
	for name, mutate := range map[string]func(*Expect){
		"no cert":        func(e *Expect) { e.Cert = nil },
		"no replay":      func(e *Expect) { e.Replay = nil },
		"no issuer":      func(e *Expect) { e.Issuer = "" },
		"no audience":    func(e *Expect) { e.Audience = "" },
		"no destination": func(e *Expect) { e.Destination = "" },
		"no request":     func(e *Expect) { e.RequestID = "" },
	} {
		exp := full
		mutate(&exp)
		if _, err := Verify(body, exp); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestTheErrorNamesNoValue(t *testing.T) {
	p := newTestIDP(t)
	exp := p.expectFor(NewReplay())
	m := defaultMinted(exp)
	m.issuer = "https://evil.example.com"
	_, err := Verify(p.build(t, m), exp)
	if err == nil {
		t.Fatal("a wrong issuer was accepted")
	}
	if contains(err.Error(), "evil.example.com") {
		t.Fatalf("the error repeats the issuer: %v", err)
	}
	for _, e := range []error{ErrSignature, ErrParse, ErrReplay, ErrNoAudience} {
		if contains(e.Error(), "example.com") {
			t.Errorf("%v names a value", e)
		}
	}
}

func TestAMissingBearerConfirmationIsRefused(t *testing.T) {
	// A holder-of-key assertion carries no bearer confirmation, and this service
	// provider does not implement one. The fixture is signed after the change, so
	// the confirmation is what fails and not the signature.
	p := newTestIDP(t)
	exp := p.expectFor(NewReplay())
	m := defaultMinted(exp)
	m.confirmationMethod = "urn:oasis:names:tc:SAML:2.0:cm:holder-of-key"
	if _, err := Verify(p.build(t, m), exp); !errors.Is(err, ErrNotBearer) {
		t.Fatalf("err %v", err)
	}
}

func TestADestinationBesideOursIsRefused(t *testing.T) {
	p := newTestIDP(t)
	exp := p.expectFor(NewReplay())
	m := defaultMinted(exp)
	m.destination = "https://gw.example.com/api/auth/saml/acs2"
	if _, err := Verify(p.build(t, m), exp); !errors.Is(err, ErrDestination) {
		t.Fatalf("err %v", err)
	}
}

func TestAFailedStatusIsRefused(t *testing.T) {
	p := newTestIDP(t)
	exp := p.expectFor(NewReplay())
	m := defaultMinted(exp)
	raw := replaceOnce(m.render(t, p), statusSuccess, "urn:oasis:names:tc:SAML:2.0:status:Requester")
	if _, err := Verify(encodeBody(raw), exp); !errors.Is(err, ErrStatus) && !errors.Is(err, ErrSignature) {
		t.Fatalf("err %v", err)
	}
}

// replaceOnce swaps the first occurrence of old for new, so a test can tamper
// with one attribute of a fixture.
func replaceOnce(s, old, new string) string {
	i := rawIndex(s, old)
	if i == 0 && (len(old) == 0 || len(s) < len(old) || s[:len(old)] != old) {
		return s
	}
	return s[:i] + new + s[i+len(old):]
}

func TestASignedAssertionHiddenInAdviceIsNotTheIdentity(t *testing.T) {
	// The wrapping attack, stated as a property: whatever the verifier accepts,
	// the address must be the one the signed assertion carries. Here the honest
	// assertion is the one signed, so a forged sibling must never win — and the
	// forged one is the one a naive reader picks, because it is the element a
	// selector returns first.
	p := newTestIDP(t)
	exp := p.expectFor(NewReplay())
	m := defaultMinted(exp)
	m.advice = true
	m.extraAssertions = []string{m.forgedAssertionXML()}

	got, err := Verify(p.build(t, m), exp)
	if err != nil {
		// Refusing the document outright is a correct answer too.
		return
	}
	email, _, ok := Email(got, Settings{})
	if !ok || email != "ops@example.com" {
		t.Fatalf("identity %q; the signed assertion carries ops@example.com", email)
	}
}
