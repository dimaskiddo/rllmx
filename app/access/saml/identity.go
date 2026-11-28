package saml

import (
	"strings"
)

// Attribute spellings an identity provider uses for an email address, in the
// order they are tried. The configured attribute name is tried first.
var emailAttributes = []string{
	"email", "mail", "emailAddress", "EmailAddress", "urn:oid:0.9.2342.19200300.100.1.3",
	"http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress",
	"urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress",
	"http://schemas.microsoft.com/ws/2008/06/identity/claims/emailaddress",
}

// NameAttribute spellings, in the same order.
var nameAttributes = []string{
	"displayName", "name", "cn", "commonName",
	"http://schemas.xmlsoap.org/ws/2005/05/identity/claims/name",
	"http://schemas.xmlsoap.org/ws/2005/05/identity/claims/nameidentifier",
	"http://schemas.microsoft.com/ws/2008/06/identity/claims/displayname",
}

// maxClaimBytes bounds one claim value. A larger one is ignored rather than
// truncated, because a truncated address is a different address.
const maxClaimBytes = 320

// Email picks the address a sign-in is for. exactOnly reports that it came from
// the NameID rather than a mail attribute, so only a listed spelling admits it:
// a NameID is whatever the provider made it. unproven marks an address from a
// common attribute spelling rather than the one the operator configured:
// Entra's `mail` carries a guest's user-editable address.
func Email(a Assertion, set Settings) (addr string, exactOnly, unproven bool, ok bool) {
	if v, ok := attribute(a, set.AttributeEmail, emailAttributes); ok && looksLikeEmail(v) {
		// The operator's own attribute is trusted; a missing one that fell back to
		// a common spelling is not.
		_, configured := valueOf(a, set.AttributeEmail)
		configured = set.AttributeEmail != "" && configured
		return v, false, !configured, true
	}
	if v := strings.TrimSpace(a.Subject.NameID.Value); looksLikeEmail(v) {
		return v, true, true, true
	}
	return "", false, false, false
}

// Name is the full name the assertion carries, or empty when it carries none.
// The NameID is not a fallback: it is an identifier, and a provider may make it
// opaque. It is never used for access.
func Name(a Assertion, set Settings) string {
	v, _ := attribute(a, set.AttributeName, nameAttributes)
	return v
}

// attribute returns the first value of the configured attribute, else of the
// first spelling that is present.
func attribute(a Assertion, configured string, fallbacks []string) (string, bool) {
	if configured != "" {
		if v, ok := valueOf(a, configured); ok {
			return v, true
		}
	}
	for _, name := range fallbacks {
		if v, ok := valueOf(a, name); ok {
			return v, true
		}
	}
	return "", false
}

// valueOf finds one attribute by name and returns its first usable value.
func valueOf(a Assertion, name string) (string, bool) {
	for _, attr := range a.Attributes {
		if !strings.EqualFold(strings.TrimSpace(attr.Name), name) {
			continue
		}
		v := attr.Value()
		if v == "" || len(v) > maxClaimBytes {
			continue
		}
		return v, true
	}
	return "", false
}

// looksLikeEmail is the loose shape check every provider's attributes need: one
// @, something either side, nothing that cannot be in an address.
func looksLikeEmail(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" || len(v) > maxClaimBytes || strings.Count(v, "@") != 1 {
		return false
	}
	at := strings.IndexByte(v, '@')
	if at == 0 || at == len(v)-1 {
		return false
	}
	for i := 0; i < len(v); i++ {
		switch c := v[i]; {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '@', c == '.', c == '_', c == '-', c == '+':
		default:
			return false
		}
	}
	return true
}
