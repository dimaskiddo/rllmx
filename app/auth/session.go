// Package auth holds the dashboard login: the session cookie, the password
// rules, the login limiter, the sign-in-mode resolver and the authenticator the
// guard asks who is calling.
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const (
	// SessionTTL is how long a dashboard session lasts: 24 hours.
	SessionTTL = 24 * time.Hour
	// Issuer goes in every token. It is the reference's name, kept so a cookie
	// from an earlier install still validates.
	Issuer = "rllm"
	// Leeway is how far a client clock may be off when `exp` is checked.
	Leeway = 30 * time.Second
	// Methods a session names in its `method` claim.
	MethodPassword = "password"
	MethodOIDC     = "oidc"
	MethodSAML     = "saml"
)

// ErrSessionMalformed is returned for a token this package cannot read.
var ErrSessionMalformed = errors.New("auth: the session token is not a signed HS256 token")

// Claims is what a session carries.
type Claims struct {
	Authenticated bool   `json:"authenticated"`
	Method        string `json:"method,omitempty"`
	Email         string `json:"email,omitempty"`
	Name          string `json:"name,omitempty"`
	// PV ties the token to a password hash and a session epoch, so a password
	// change or a "logout everywhere" ends every session at once.
	PV  string `json:"pv,omitempty"`
	Iss string `json:"iss"`
	Iat int64  `json:"iat"`
	Exp int64  `json:"exp"`
}

type header struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
}

// Sign returns the HS256 token for claims, filling Iss, Iat and Exp. The method
// is fixed: a token whose alg says anything else is refused on the way back.
func Sign(secret []byte, c Claims) (string, error) {
	if len(secret) == 0 {
		return "", errors.New("auth: no session secret")
	}
	c.Iss = Issuer
	c.Exp = time.Now().Add(SessionTTL).Unix()
	c.Iat = time.Now().Unix()

	head, err := json.Marshal(header{Alg: "HS256", Typ: "JWT"})
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	signing := b64(head) + "." + b64(body)
	return signing + "." + string(sig(secret, signing)), nil
}

// Verify checks the signature, the pinned method, the issuer and the expiry,
// and returns the claims. A token that fails any check is not a session.
func Verify(secret []byte, token string, now time.Time) (Claims, bool) {
	if len(secret) == 0 || token == "" {
		return Claims{}, false
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Claims{}, false
	}

	rawHead, err := dec(parts[0])
	if err != nil {
		return Claims{}, false
	}
	var h header
	if json.Unmarshal(rawHead, &h) != nil || h.Alg != "HS256" {
		return Claims{}, false
	}

	want := sig(secret, parts[0]+"."+parts[1])
	if subtle.ConstantTimeCompare(want, []byte(parts[2])) != 1 {
		return Claims{}, false
	}

	rawBody, err := dec(parts[1])
	if err != nil {
		return Claims{}, false
	}
	var c Claims
	if json.Unmarshal(rawBody, &c) != nil {
		return Claims{}, false
	}
	if !c.Authenticated || c.Iss != Issuer || c.Exp == 0 {
		return Claims{}, false
	}
	if now.After(time.Unix(c.Exp, 0).Add(Leeway)) {
		return Claims{}, false
	}
	return c, true
}

// sig is the HMAC over the two encoded segments.
func sig(secret []byte, signing string) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(signing))
	return []byte(base64.RawURLEncoding.EncodeToString(mac.Sum(nil)))
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func dec(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }
