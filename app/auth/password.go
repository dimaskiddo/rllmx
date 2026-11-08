package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"strings"

	frameworkhash "github.com/goravel/framework/contracts/hash"
)

const (
	// DefaultPassword is what a fresh install accepts while no hash is stored.
	DefaultPassword = "123456"
	minPassword     = 8
	maxPassword     = 72
	pvLen           = 16
)

// ErrPasswordTooShort and the rest are the new-password rules PRD 2 states. Each
// error names the rule, never the value.
var (
	ErrPasswordShort   = errors.New("auth: the new password must be at least 8 bytes")
	ErrPasswordLong    = errors.New("auth: the new password must be at most 72 bytes")
	ErrPasswordDefault = errors.New("auth: the new password must not be a well-known default")
)

// Passwords hashes and checks the dashboard password through the framework's
// bcrypt hasher, whose cost comes from config/hashing.go.
type Passwords struct {
	hash frameworkhash.Hash
	// Initial is AUTH_INITIAL_PASSWORD, compared in constant time while no hash
	// is stored.
	Initial string
}

// NewPasswords builds the password checker.
func NewPasswords(h frameworkhash.Hash, initial string) Passwords {
	return Passwords{hash: h, Initial: initial}
}

// Judge applies the new-password rule to a candidate.
func Judge(password string) error {
	switch {
	case len(password) < minPassword:
		return ErrPasswordShort
	case len(password) > maxPassword:
		return ErrPasswordLong
	case wellKnown(password):
		return ErrPasswordDefault
	}
	return nil
}

// wellKnown reports a password every deployment of this product starts with.
func wellKnown(password string) bool {
	for _, p := range []string{DefaultPassword, "change-me"} {
		if subtle.ConstantTimeCompare([]byte(password), []byte(p)) == 1 {
			return true
		}
	}
	return false
}

// Hash returns the stored form of a judged password.
func (p Passwords) Hash(password string) (string, error) {
	if p.hash == nil {
		return "", errors.New("auth: no password hasher")
	}
	return p.hash.Make(password)
}

// Check reports whether password matches the stored hash, or the initial
// password while no hash is stored. An empty answer never passes.
func (p Passwords) Check(stored, password string) bool {
	if password == "" {
		return false
	}
	if stored != "" {
		if p.hash == nil {
			return false
		}
		return p.hash.Check(password, stored)
	}
	initial := p.Initial
	if initial == "" {
		initial = DefaultPassword
	}
	return subtle.ConstantTimeCompare([]byte(password), []byte(initial)) == 1
}

// WellKnown reports whether the deployment still accepts a password everybody
// knows: no hash stored, or a hash of one of the defaults.
func (p Passwords) WellKnown(stored string) bool {
	if stored == "" {
		initial := p.Initial
		if initial == "" {
			return true
		}
		return wellKnown(initial)
	}
	if p.hash == nil {
		return false
	}
	for _, candidate := range []string{DefaultPassword, "change-me"} {
		if p.hash.Check(candidate, stored) {
			return true
		}
	}
	return false
}

// PV is what a session carries to be revocable: the first 16 hex of an HMAC
// over the stored hash and the session epoch, so a password change or an epoch
// bump invalidates every token issued before it.
func PV(secret []byte, storedHash, epoch string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(storedHash))
	mac.Write([]byte("|"))
	mac.Write([]byte(epoch))
	return hex.EncodeToString(mac.Sum(nil))[:pvLen]
}

// SamePV compares a token's claim against the current value in constant time.
func SamePV(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// ValidName reports whether a key name is one this product accepts: at most 64
// bytes and no control character, so it cannot forge a terminal line.
func ValidName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 64 {
		return false
	}
	for i := 0; i < len(name); i++ {
		if name[i] < 0x20 || name[i] == 0x7f {
			return false
		}
	}
	return true
}
