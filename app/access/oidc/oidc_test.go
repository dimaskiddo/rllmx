package oidc

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"rllmx/app/store/document"
)

const (
	testIssuer   = "https://idp.example.com"
	testClientID = "client-abc"
)

// signingKey is one provider key and the JWKS that publishes it.
type signingKey struct {
	signer jose.Signer
	set    *jose.JSONWebKeySet
}

// rsaKey builds an RS256 signer published under kid.
func rsaKey(t *testing.T, kid string) signingKey {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return signingKey{
		signer: signerFor(t, jose.RS256, priv, kid),
		set:    &jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &priv.PublicKey, KeyID: kid, Algorithm: string(jose.RS256), Use: "sig"}}},
	}
}

// ecdsaKey builds an ES256 signer published under kid.
func ecdsaKey(t *testing.T, kid string) signingKey {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return signingKey{
		signer: signerFor(t, jose.ES256, priv, kid),
		set:    &jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &priv.PublicKey, KeyID: kid, Algorithm: string(jose.ES256), Use: "sig"}}},
	}
}

func signerFor(t *testing.T, alg jose.SignatureAlgorithm, key any, kid string) jose.Signer {
	t.Helper()
	s, err := jose.NewSigner(jose.SigningKey{Algorithm: alg, Key: key}, (&jose.SignerOptions{}).WithHeader("kid", kid).WithType("JWT"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// token signs a claim map with the key and returns the compact form.
func token(t *testing.T, k signingKey, claims map[string]any) string {
	t.Helper()
	s, err := jwt.Signed(k.signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func validClaims() map[string]any {
	return map[string]any{
		"iss":            testIssuer,
		"sub":            "user-1",
		"aud":            testClientID,
		"exp":            time.Now().Add(5 * time.Minute).Unix(),
		"iat":            time.Now().Unix(),
		"nonce":          "nonce-1",
		"email":          "ops@example.com",
		"email_verified": true,
		"name":           "Ops",
	}
}

func expect() Expect {
	return Expect{ClientID: testClientID, Nonce: "nonce-1", Now: time.Now()}
}

func TestVerifyAcceptsRS256AndES256(t *testing.T) {
	for name, k := range map[string]signingKey{"rs256": rsaKey(t, "k1"), "es256": ecdsaKey(t, "k1")} {
		got, err := VerifyIDToken(token(t, k, validClaims()), k.set, testIssuer, expect())
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got.Subject != "user-1" || got.Email != "ops@example.com" {
			t.Fatalf("%s: %+v", name, got)
		}
	}
}

func TestVerifyRefusesEveryOtherAlgorithm(t *testing.T) {
	// A token signed with HS256 and the public key as the secret: the classic
	// key-confusion attempt. The parser's allow list refuses it.
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	hmac, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.HS256, Key: []byte(strings.Repeat("k", 32))}, (&jose.SignerOptions{}).WithHeader("kid", "k1").WithType("JWT"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := jwt.Signed(hmac).Claims(validClaims()).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	set := &jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &priv.PublicKey, KeyID: "k1"}}}
	if _, err := VerifyIDToken(raw, set, testIssuer, expect()); err == nil {
		t.Fatal("an HS256 token verified against an RSA key")
	}

	none := base64Header(`{"alg":"none","typ":"JWT"}`)
	if _, err := VerifyIDToken(none, set, testIssuer, expect()); err == nil {
		t.Fatal("an alg=none token verified")
	}
}

func TestVerifyRefusesAForeignSignature(t *testing.T) {
	good := rsaKey(t, "k1")
	other := rsaKey(t, "k1")
	raw := token(t, other, validClaims())
	if _, err := VerifyIDToken(raw, good.set, testIssuer, expect()); err == nil {
		t.Fatal("a token signed by another key verified")
	}
}

func TestVerifyClaimRules(t *testing.T) {
	k := rsaKey(t, "k1")
	cases := []struct {
		name   string
		mutate func(map[string]any)
		exp    Expect
		want   error
	}{
		{"another issuer", func(c map[string]any) { c["iss"] = "https://evil.example.com" }, expect(), ErrWrongIssuer},
		{"another audience", func(c map[string]any) { c["aud"] = "another-client" }, expect(), ErrWrongAudience},
		{"no audience", func(c map[string]any) { delete(c, "aud") }, expect(), ErrWrongAudience},
		{"several audiences without azp", func(c map[string]any) {
			c["aud"] = []string{testClientID, "other"}
		}, expect(), ErrWrongAudience},
		{"several audiences with a wrong azp", func(c map[string]any) {
			c["aud"] = []string{testClientID, "other"}
			c["azp"] = "other"
		}, expect(), ErrWrongAudience},
		{"several audiences with the right azp", func(c map[string]any) {
			c["aud"] = []string{testClientID, "other"}
			c["azp"] = testClientID
		}, expect(), nil},
		{"expired", func(c map[string]any) { c["exp"] = time.Now().Add(-2 * time.Minute).Unix() }, expect(), ErrExpired},
		{"no expiry", func(c map[string]any) { delete(c, "exp") }, expect(), ErrExpired},
		{"not yet valid", func(c map[string]any) { c["nbf"] = time.Now().Add(time.Hour).Unix() }, expect(), ErrExpired},
		{"inside the skew", func(c map[string]any) { c["exp"] = time.Now().Add(-30 * time.Second).Unix() }, expect(), nil},
		{"another nonce", func(c map[string]any) { c["nonce"] = "nonce-2" }, expect(), ErrNonce},
		{"no nonce", func(c map[string]any) { delete(c, "nonce") }, expect(), ErrNonce},
		{"no subject", func(c map[string]any) { c["sub"] = "" }, expect(), ErrSubject},
		{"unverified", func(c map[string]any) { c["email_verified"] = false }, expect(), ErrUnverified},
		{"unverified as a string", func(c map[string]any) { c["email_verified"] = "false" }, expect(), ErrUnverified},
		{"verified as a string", func(c map[string]any) { c["email_verified"] = "true" }, expect(), nil},
		{"no email_verified claim", func(c map[string]any) { delete(c, "email_verified") }, expect(), nil},
	}
	for _, tc := range cases {
		claims := validClaims()
		tc.mutate(claims)
		_, err := VerifyIDToken(token(t, k, claims), k.set, testIssuer, tc.exp)
		if tc.want == nil && err != nil {
			t.Errorf("%s: %v", tc.name, err)
		}
		if tc.want != nil && !errorsIs(err, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
	}
}

func TestVerifyChecksTheAccessTokenHash(t *testing.T) {
	k := rsaKey(t, "k1")
	claims := validClaims()
	claims["at_hash"] = atHash("an-access-token")

	if _, err := VerifyIDToken(token(t, k, claims), k.set, testIssuer, Expect{ClientID: testClientID, Nonce: "nonce-1", AccessToken: "an-access-token"}); err != nil {
		t.Fatalf("the right access token: %v", err)
	}
	if _, err := VerifyIDToken(token(t, k, claims), k.set, testIssuer, Expect{ClientID: testClientID, Nonce: "nonce-1", AccessToken: "another-token"}); err == nil {
		t.Fatal("a wrong access token passed at_hash")
	}
	if _, err := VerifyIDToken(token(t, k, claims), k.set, testIssuer, expect()); err == nil {
		t.Fatal("an at_hash with nothing to compare was accepted")
	}
}

func TestVerifyRefusesATokenWithNoMatchingKey(t *testing.T) {
	k := rsaKey(t, "k1")
	set := &jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: k.set.Keys[0].Key, KeyID: "other"}}}
	if _, err := VerifyIDToken(token(t, k, validClaims()), set, testIssuer, expect()); err == nil {
		t.Fatal("a token whose kid matches no key verified")
	}
	if _, err := VerifyIDToken(token(t, k, validClaims()), &jose.JSONWebKeySet{}, testIssuer, expect()); err == nil {
		t.Fatal("an empty key set verified a token")
	}
}

func TestVerifyRefusesAKeyMarkedForEncryption(t *testing.T) {
	k := rsaKey(t, "k1")
	set := &jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: k.set.Keys[0].Key, KeyID: "k1", Use: "enc"}}}
	if _, err := VerifyIDToken(token(t, k, validClaims()), set, testIssuer, expect()); err == nil {
		t.Fatal("an encryption key verified a signature")
	}
}

func TestVerifyAcceptsATokenWithNoKidAgainstOneKey(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	s, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: priv}, (&jose.SignerOptions{}).WithType("JWT"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := jwt.Signed(s).Claims(validClaims()).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	set := &jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &priv.PublicKey}}}
	if _, err := VerifyIDToken(raw, set, testIssuer, expect()); err != nil {
		t.Fatalf("a single-key provider was refused: %v", err)
	}
}

func TestVerifyRefusesSomethingThatIsNotThreeSegments(t *testing.T) {
	k := rsaKey(t, "k1")
	for _, raw := range []string{"", "a.b", "a.b.c.d"} {
		if _, err := VerifyIDToken(raw, k.set, testIssuer, expect()); err == nil {
			t.Errorf("%q verified", raw)
		}
	}
}

func TestIssuerRules(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"https://idp.example.com", true},
		{"https://idp.example.com:8443", true},
		{"HTTPS://IDP.example.com", true},
		{"http://localhost:9999", true},
		{"http://127.0.0.1:9999", true},
		{"http://idp.example.com", false},
		{"https://user:pass@idp.example.com", false},
		{"https://idp.example.com/?x=1", false},
		{"https://idp.example.com/#f", false},
		{"https://idp.example.com/tenant", false},
		{"ftp://idp.example.com", false},
		{"", false},
		{"not a url", false},
	}
	for _, tc := range cases {
		got, err := ParseIssuer(tc.raw)
		if (err == nil) != tc.want {
			t.Errorf("%q: %v, want ok=%v", tc.raw, err, tc.want)
		}
		if tc.want && got == "" {
			t.Errorf("%q returned an empty canonical form", tc.raw)
		}
	}
	if _, err := ParseIssuer("https://idp.example.com"); err != nil {
		t.Fatal(err)
	}
}

func TestScopesForceOpenIDAndCapAtEight(t *testing.T) {
	got := Scopes("")
	if len(got) != 3 || got[0] != "openid" {
		t.Fatalf("%v", got)
	}
	if got := Scopes("profile email"); got[0] != "openid" {
		t.Fatalf("%v", got)
	}
	if got := Scopes("openid openid profile"); len(got) != 2 {
		t.Fatalf("%v", got)
	}
	many := Scopes("a b c d e f g h i j k")
	if len(many) != MaxScopes {
		t.Fatalf("%d scopes", len(many))
	}
}

func TestSettingsConfiguredAndValidated(t *testing.T) {
	d, err := document.ParseDoc(`{"oidcIssuerUrl":"https://idp.example.com","oidcClientId":"c","oidcClientSecret":"s","oidcScopes":"openid profile email","oidcLoginLabel":"Sign in"}`)
	if err != nil {
		t.Fatal(err)
	}
	s := FromDoc(d)
	if !s.Configured() || s.LoginLabel != "Sign in" {
		t.Fatalf("%+v", s)
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Settings){
		"no id":      func(s *Settings) { s.ClientID = "" },
		"no secret":  func(s *Settings) { s.ClientSecret = "" },
		"no issuer":  func(s *Settings) { s.Issuer = "" },
		"plain http": func(s *Settings) { s.Issuer = "http://idp.example.com" },
		"bad scopes": func(s *Settings) { s.Scopes = "openid a b c d e f g h" },
	} {
		copy := s
		mutate(&copy)
		if name == "no id" || name == "no secret" || name == "no issuer" {
			if copy.Configured() {
				t.Errorf("%s: still configured", name)
			}
			continue
		}
		if err := copy.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestEmailFallsBackToPreferredUsername(t *testing.T) {
	if got, exact, ok := Email(Claims{Email: "a@b.test", PreferredUsername: "other.test"}); !ok || exact || got != "a@b.test" {
		t.Fatalf("%q %v %v", got, exact, ok)
	}
	if got, exact, ok := Email(Claims{PreferredUsername: "a@b.test"}); !ok || !exact || got != "a@b.test" {
		t.Fatalf("%q %v %v: a preferred_username address must be exact-only", got, exact, ok)
	}
	for _, bad := range []string{"", "no-at", "two@at@test", "@test", "user@"} {
		if _, _, ok := Email(Claims{PreferredUsername: bad}); ok {
			t.Errorf("%q became an address", bad)
		}
	}
}

func TestDisplayNamePicksAFallback(t *testing.T) {
	if got := DisplayName(Claims{Name: "Ops", Email: "a@b.test"}); got != "Ops" {
		t.Fatal(got)
	}
	if got := DisplayName(Claims{Subject: "s"}); got != "s" {
		t.Fatal(got)
	}
	if got := DisplayName(Claims{}); got != "OIDC user" {
		t.Fatal(got)
	}
}

func TestEndpointRules(t *testing.T) {
	for _, ok := range []string{"https://idp.example.com/token", "https://idp.example.com/token?tenant=1", "http://127.0.0.1:9/token"} {
		if _, err := ParseEndpoint(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "http://idp.example.com/token", "/token", "javascript:alert(1)"} {
		if _, err := ParseEndpoint(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func atHash(access string) string {
	sum := sha256.Sum256([]byte(access))
	return base64.RawURLEncoding.EncodeToString(sum[:len(sum)/2])
}

func base64Header(h string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(h)) + "." +
		base64.RawURLEncoding.EncodeToString([]byte(`{"iss":"`+testIssuer+`"}`)) + "."
}

func errorsIs(err, want error) bool { return errors.Is(err, want) }
