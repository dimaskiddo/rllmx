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

// DisplayName picks what the dashboard shows. It is never used for access.
func DisplayName(c Claims) string {
	for _, v := range []string{c.Name, c.Email, c.PreferredUsername, c.Subject} {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return "OIDC user"
}
