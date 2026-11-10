package sso

import "strings"

// Allow is who may sign in: exact email addresses and @domain entries, comma
// separated, no wildcards. An empty list admits every account the provider
// accepts, which is the operator's choice to make.
type Allow struct {
	exact   map[string]bool
	domains map[string]bool
	empty   bool
}

// ParseAllow reads AUTH_SSO_ALLOW. Scopes are case-folded, because an email
// domain is case-insensitive in practice at every provider.
func ParseAllow(raw string) Allow {
	a := Allow{exact: map[string]bool{}, domains: map[string]bool{}}
	seen := false
	for entry := range strings.SplitSeq(raw, ",") {
		entry = strings.ToLower(strings.TrimSpace(entry))
		if entry == "" {
			continue
		}
		seen = true
		if domain, ok := strings.CutPrefix(entry, "@"); ok && domain != "" {
			a.domains[domain] = true
			continue
		}
		a.exact[entry] = true
	}
	a.empty = !seen
	return a
}

// Empty reports whether the list admits everyone.
func (a Allow) Empty() bool { return a.empty }

// Allows reports whether an address may sign in. exactOnly refuses a @domain
// match, which is what an address taken from a user-editable claim gets: the
// claim is not proof of the domain, so only a listed spelling counts.
func (a Allow) Allows(email string, exactOnly bool) bool {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return false
	}
	if a.empty {
		return true
	}
	if a.exact[email] {
		return true
	}
	if exactOnly {
		return false
	}
	at := strings.LastIndexByte(email, '@')
	if at < 0 {
		return false
	}
	return a.domains[email[at+1:]]
}

// Len reports how many entries the list holds.
func (a Allow) Len() int { return len(a.exact) + len(a.domains) }
