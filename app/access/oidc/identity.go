package oidc

import (
	"strings"
)

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
