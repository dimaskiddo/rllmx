package oidc

import (
	"strings"
)

// MailUnproven reports a token whose `email` rests on no `email_verified`
// claim: some providers (Entra v2) send none, and an address they let a user
// edit is then no proof of the domain. The sign-in treats it as exact-only, the
// way it treats `preferred_username` for the admin match. It still signs in as a
// standard user, because a provider that never sends the claim would otherwise
// lock every user out.
func MailUnproven(c Claims) bool {
	return strings.TrimSpace(c.Email) != "" && len(c.EmailVerified) == 0
}

// Email picks the address a sign-in is for. `email` wins. When it is absent,
// `preferred_username` counts only if it holds exactly one `@`, because that
// claim is a user name at some providers and an address at others.
func Email(c Claims) (addr string, exactOnly bool, ok bool) {
	if e := strings.TrimSpace(c.Email); e != "" {
		return e, false, true
	}
	p := strings.TrimSpace(c.PreferredUsername)
	if strings.Count(p, "@") != 1 || strings.HasPrefix(p, "@") || strings.HasSuffix(p, "@") {
		return "", false, false
	}
	return p, true, true
}

// FullName is the name the provider reports, or empty when it reports none. It
// has no fallback: a fallback would overwrite a stored full name with an
// address on every sign-in. It is never used for access.
func FullName(c Claims) string {
	return strings.TrimSpace(c.Name)
}
