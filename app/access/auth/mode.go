package auth

// Mode is how the dashboard may be signed into. The values are the reference's
// `authMode` settings, resolved into what is actually possible.
type Mode struct {
	// Password is true when the password form is offered.
	Password bool
	// OIDC and SAML are true when that single sign-on type may be used.
	OIDC bool
	SAML bool
}

// ModeNames are the authMode values the settings row may hold. The settings API
// and the auth:mode command both check against this list.
func ModeNames() []string { return []string{"password", "oidc", "saml", "sso", "both"} }

// SSO reports whether a single sign-on button is shown.
func (m Mode) SSO() bool { return m.OIDC || m.SAML }

// ResolveMode turns the `authMode` and `ssoType` settings into one decision.
// Every mode keeps password login while nothing else is usable, so a
// half-configured single sign-on never locks the administrator out. The allow
// list does not decide this: a configured provider is usable on its own, and an
// empty AUTH_SSO_ALLOW admits every account the provider accepts.
func ResolveMode(authMode, ssoType string, oidcUsable, samlUsable bool) Mode {
	var m Mode
	switch authMode {
	case "oidc":
		m.OIDC = oidcUsable
	case "saml":
		m.SAML = samlUsable
	case "sso", "both":
		switch ssoType {
		case "saml":
			m.SAML = samlUsable
		default:
			m.OIDC = oidcUsable
		}
		if authMode == "both" {
			m.OIDC, m.SAML = oidcUsable, samlUsable
		}
	}
	m.Password = !m.SSO()
	return m
}
