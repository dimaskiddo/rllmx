package sso

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"

	"github.com/goravel/framework/contracts/log"
)

// The fixed failure codes. Every one is a constant, so a provider's own message
// can never become a query parameter or a log line.
const (
	CodeDisabled      = "sso_disabled"
	CodeNotConfigured = "sso_not_configured"
	CodeBusy          = "sso_busy"
	CodeExpired       = "sso_expired"
	CodeInvalid       = "sso_invalid"
	CodeNoAccount     = "sso_no_account"
	CodeNoEmail       = "sso_no_email"
	CodeUnverified    = "sso_unverified"
	CodeIDPError      = "sso_idp_error"
)

// LoginURL builds the redirect a failure answers with. The base comes from
// PublicBase; path is the login page.
func LoginURL(base, code string) string {
	return CleanURL(base, "/login") + "?error=" + url.QueryEscape(code)
}

// Debugf logs provider text at DEBUG only, after the redactor has seen it. It
// exists so a caller has no reason to put that text in a code or a response.
func Debugf(l log.Log, format string, args ...any) {
	if l == nil {
		return
	}
	l.Debugf(format, args...)
}

// ShortHash is what may be logged about an issuer, an entity id or a subject: a
// short digest, so an operator can correlate two lines without the value
// reaching a file.
func ShortHash(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])[:12]
}
