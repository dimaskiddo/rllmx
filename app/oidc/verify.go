package oidc

import (
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// Skew is how far a provider's clock may be off when `exp` and `nbf` are
// checked.
const Skew = 60 * time.Second

// The verification failures. Each one maps to a fixed login error code, and none
// carries a claim value.
var (
	ErrTokenMalformed = errors.New("oidc: the ID token is not a signed JWT")
	ErrSignature      = errors.New("oidc: the ID token signature did not verify")
	ErrWrongIssuer    = errors.New("oidc: the ID token names another issuer")
	ErrWrongAudience  = errors.New("oidc: the ID token was not issued for this client")
	ErrExpired        = errors.New("oidc: the ID token is outside its validity window")
	ErrNonce          = errors.New("oidc: the ID token does not answer this sign-in")
	ErrSubject        = errors.New("oidc: the ID token names no subject")
	ErrUnverified     = errors.New("oidc: the provider says the address is not verified")
	ErrAtHash         = errors.New("oidc: the at_hash claim does not match")
)

// Claims is the part of an ID token this program reads. `aud` and
// `email_verified` are raw because both arrive as either a JSON type or a
// string, and a decoder that guesses once guesses wrong on the other provider.
type Claims struct {
	Issuer            string          `json:"iss"`
	Subject           string          `json:"sub"`
	Audience          json.RawMessage `json:"aud"`
	AuthorizedParty   string          `json:"azp"`
	Expiry            int64           `json:"exp"`
	NotBefore         int64           `json:"nbf"`
	IssuedAt          int64           `json:"iat"`
	Nonce             string          `json:"nonce"`
	AtHash            string          `json:"at_hash"`
	Email             string          `json:"email"`
	EmailVerified     json.RawMessage `json:"email_verified"`
	PreferredUsername string          `json:"preferred_username"`
	Name              string          `json:"name"`
}

// Expect is what this sign-in demands of the token.
type Expect struct {
	ClientID string
	Nonce    string
	// AccessToken is compared against `at_hash` when the token carries one.
	AccessToken string
	Now         time.Time
}

// VerifyIDToken parses and checks an ID token. The algorithm is pinned by the
// parser's allow list, so a token that says `none` or HS256 is refused before a
// key is even selected.
func VerifyIDToken(token string, set *jose.JSONWebKeySet, issuer string, exp Expect) (Claims, error) {
	if strings.Count(token, ".") != 2 {
		return Claims{}, ErrTokenMalformed
	}
	parsed, err := jwt.ParseSigned(token, []jose.SignatureAlgorithm{jose.RS256, jose.ES256})
	if err != nil {
		return Claims{}, ErrTokenMalformed
	}
	if len(parsed.Headers) != 1 {
		return Claims{}, ErrTokenMalformed
	}
	key, err := pickKey(parsed.Headers[0], set)
	if err != nil {
		return Claims{}, ErrSignature
	}

	var claims Claims
	if err := parsed.Claims(key, &claims); err != nil {
		return Claims{}, ErrSignature
	}
	if err := check(claims, issuer, exp); err != nil {
		return Claims{}, err
	}
	return claims, nil
}

// pickKey selects the one key the header names. A token with no `kid` may try
// every key in the set, which is what a provider with one key sends.
func pickKey(h jose.Header, set *jose.JSONWebKeySet) (any, error) {
	if set == nil || len(set.Keys) == 0 {
		return nil, errors.New("oidc: the provider published no keys")
	}
	kid := h.KeyID
	if kid == "" {
		for _, k := range set.Keys {
			if k.Valid() && usable(k, h.Algorithm) {
				return k.Key, nil
			}
		}
		return nil, errors.New("oidc: no usable key")
	}
	for _, k := range set.Key(kid) {
		if k.Valid() && usable(k, h.Algorithm) {
			return k.Key, nil
		}
	}
	return nil, errors.New("oidc: no key matches the token")
}

// usable refuses a key the provider marked for encryption or for a signature
// algorithm other than the one the token names.
func usable(k jose.JSONWebKey, alg string) bool {
	if k.Use != "" && k.Use != "sig" {
		return false
	}
	if k.Algorithm != "" && k.Algorithm != alg {
		return false
	}
	switch k.Key.(type) {
	case *rsa.PublicKey, *ecdsa.PublicKey:
		return true
	}
	return false
}

// check applies the claim rules. These are ours: go-jose verifies the signature
// and decodes, and stops there.
func check(c Claims, issuer string, exp Expect) error {
	if c.Issuer != issuer {
		return ErrWrongIssuer
	}
	aud, err := audience(c.Audience)
	if err != nil {
		return ErrWrongAudience
	}
	if !contains(aud, exp.ClientID) {
		return ErrWrongAudience
	}
	// A token minted for several audiences must name the party it was issued to.
	if len(aud) > 1 && c.AuthorizedParty != exp.ClientID {
		return ErrWrongAudience
	}

	now := exp.Now
	if now.IsZero() {
		now = time.Now()
	}
	if c.Expiry == 0 || now.After(time.Unix(c.Expiry, 0).Add(Skew)) {
		return ErrExpired
	}
	if c.NotBefore != 0 && now.Add(Skew).Before(time.Unix(c.NotBefore, 0)) {
		return ErrExpired
	}
	if subtle.ConstantTimeCompare([]byte(c.Nonce), []byte(exp.Nonce)) != 1 || exp.Nonce == "" {
		return ErrNonce
	}
	if strings.TrimSpace(c.Subject) == "" {
		return ErrSubject
	}
	if !verified(c.EmailVerified) {
		return ErrUnverified
	}
	if c.AtHash != "" && !atHashMatches(c.AtHash, exp.AccessToken) {
		return ErrAtHash
	}
	return nil
}

// audience reads `aud`, which is one string or an array of them.
func audience(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 {
		return nil, errors.New("oidc: no audience")
	}
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return []string{one}, nil
	}
	var many []string
	if json.Unmarshal(raw, &many) == nil {
		return many, nil
	}
	return nil, errors.New("oidc: the audience is not a string or an array")
}

// verified reports whether `email_verified` is absent or true. Both the boolean
// and the string form are read, because providers send both.
func verified(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		return b
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "", "true":
			return true
		}
		return false
	}
	return false
}

// atHashMatches checks the left half of the access token's SHA-256, per the
// OpenID Connect core spec. A token that carries no access token to compare is
// refused, because the claim exists only to prove the pair.
func atHashMatches(claimed, accessToken string) bool {
	if accessToken == "" {
		return false
	}
	sum := sha256.Sum256([]byte(accessToken))
	want := base64.RawURLEncoding.EncodeToString(sum[:len(sum)/2])
	return subtle.ConstantTimeCompare([]byte(claimed), []byte(want)) == 1
}

func contains(list []string, item string) bool {
	for _, v := range list {
		if v == item {
			return true
		}
	}
	return false
}
