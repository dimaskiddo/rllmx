package sso

import "strings"

// AdminList is who signs in as the administrator through the identity provider:
// exact email addresses and @domain entries, comma separated, no wildcards. An
// address outside the list is still admitted, as a standard user; the provider
// decides who has an account at all.
type AdminList struct {
	exact   map[string]bool
	domains map[string]bool
}

// ParseAdmins reads AUTH_SSO_ADMIN. Entries are case-folded, because an email
// domain is case-insensitive in practice at every provider.
func ParseAdmins(raw string) AdminList {
	a := AdminList{exact: map[string]bool{}, domains: map[string]bool{}}
	for entry := range strings.SplitSeq(raw, ",") {
		entry = strings.ToLower(strings.TrimSpace(entry))
		if entry == "" {
			continue
		}
		if domain, ok := strings.CutPrefix(entry, "@"); ok && domain != "" {
			a.domains[domain] = true
			continue
		}
		a.exact[entry] = true
	}
	return a
}

// IsAdmin reports whether an address signs in as the administrator. exactOnly
// refuses a @domain match, which is what an address taken from a user-editable
// claim gets: the claim is not proof of the domain, so only a listed spelling
// counts. An empty list makes nobody the admin.
func (a AdminList) IsAdmin(email string, exactOnly bool) bool {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return false
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
func (a AdminList) Len() int { return len(a.exact) + len(a.domains) }
