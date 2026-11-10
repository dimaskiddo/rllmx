package oidc

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
)

// errRandom means the system random source failed, which no healthy host does.
var errRandom = errors.New("oidc: the system random source failed")

// verifierBytes is how much entropy the PKCE secret carries: 32 bytes, which is
// the RFC 7636 upper bound after base64url encoding.
const verifierBytes = 32

// Verifier returns a fresh PKCE secret in unpadded base64url.
func Verifier() (string, error) {
	b := make([]byte, verifierBytes)
	if _, err := rand.Read(b); err != nil {
		return "", errRandom
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Challenge is the S256 challenge for a verifier. S256 is the only method this
// program sends; `plain` is not offered.
func Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// Method is the challenge method the authorization request names.
const Method = "S256"
