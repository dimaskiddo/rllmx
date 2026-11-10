package sso

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strings"

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
	CodeNotAllowed    = "sso_not_allowed"
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

// Has reports whether a comma-separated value names an item exactly.
func Has(list, item string) bool {
	for entry := range strings.SplitSeq(list, ",") {
		if strings.EqualFold(strings.TrimSpace(entry), item) {
			return true
		}
	}
	return false
}

// ShortHash is what may be logged about an issuer, an entity id or a subject: a
// short digest, so an operator can correlate two lines without the value
// reaching a file.
func ShortHash(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])[:12]
}
