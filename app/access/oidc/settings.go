package oidc

import (
	"errors"
	"net/url"
	"strings"

	"rllmx/app/platform/network"
	"rllmx/app/store/document"
)

// MaxScopes caps how many scopes a provider is asked for. Beyond a handful the
// request grows without adding anything this program reads.
const MaxScopes = 8

// ErrIssuer is returned for an issuer this program will not talk to. It names no
// value.
var ErrIssuer = errors.New("oidc: the issuer must be an https URL with no credentials, query or fragment")

// Settings is what the settings row holds for OIDC.
type Settings struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	Scopes       string
	LoginLabel   string
}

// FromDoc reads the OIDC settings from the settings row. The defaults come from
// the row itself, so an untouched install reports nothing configured.
func FromDoc(d document.Doc) Settings {
	s := Settings{}
	if v, ok := d.GetString("oidcIssuerUrl"); ok {
		s.Issuer = strings.TrimSpace(v)
	}
	if v, ok := d.GetString("oidcClientId"); ok {
		s.ClientID = strings.TrimSpace(v)
	}
	if v, ok := d.GetString("oidcClientSecret"); ok {
		s.ClientSecret = v
	}
	if v, ok := d.GetString("oidcScopes"); ok {
		s.Scopes = v
	}
	if v, ok := d.GetString("oidcLoginLabel"); ok {
		s.LoginLabel = v
	}
	return s
}

// Configured reports whether a sign-in is possible at all: issuer, client id and
// client secret, because a public client with no secret is not supported.
func (s Settings) Configured() bool {
	return s.Issuer != "" && s.ClientID != "" && s.ClientSecret != ""
}

// Usable reports whether a sign-in can finish: everything Configured needs, and
// an issuer and scope list Validate accepts. The dashboard hides the password
// form only for a usable sign-in, because a configured one that discovery
// refuses would leave no way in.
func (s Settings) Usable() bool {
	return s.Configured() && s.Validate() == nil
}

// Validate checks the issuer and the scope list. The issuer must be https, or
// http only when it names this machine, which is what a local test provider
// needs; it may carry no credentials, query or fragment.
func (s Settings) Validate() error {
	if _, err := ParseIssuer(s.Issuer); err != nil {
		return err
	}
	// Count what the operator wrote, not the capped list: silently dropping a
	// scope would hide a misconfiguration.
	count := 0
	for range strings.FieldsSeq(s.Scopes) {
		count++
	}
	if count > MaxScopes {
		return errors.New("oidc: at most eight scopes")
	}
	return nil
}

// ParseIssuer returns the canonical issuer, or ErrIssuer.
func ParseIssuer(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ErrIssuer
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return "", ErrIssuer
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", ErrIssuer
	}
	if u.Path != "" && u.Path != "/" {
		return "", ErrIssuer
	}
	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "https":
	case "http":
		if !network.IsLoopbackHost(u.Hostname()) {
			return "", ErrIssuer
		}
	default:
		return "", ErrIssuer
	}
	return scheme + "://" + strings.ToLower(u.Host), nil
}

// Scopes splits the scope setting into plain tokens with `openid` forced in. An
// empty setting means the reference's default.
func Scopes(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		raw = "openid profile email"
	}
	seen := map[string]bool{}
	var out []string
	for tok := range strings.FieldsSeq(raw) {
		if seen[tok] {
			continue
		}
		seen[tok] = true
		out = append(out, tok)
	}
	if !seen["openid"] {
		out = append([]string{"openid"}, out...)
	}
	if len(out) > MaxScopes {
		out = out[:MaxScopes]
	}
	return out
}
