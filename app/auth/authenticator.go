package auth

import (
	"context"
	nethttp "net/http"
	"time"

	frameworkhash "github.com/goravel/framework/contracts/hash"
	contractshttp "github.com/goravel/framework/contracts/http"

	"rllmx/app/identity"
)

// CookieName is the dashboard session cookie.
const CookieName = "auth_token"

// RequestCookie reads a cookie from a request the logger, the guard and the
// limiters share.
func RequestCookie(r *nethttp.Request, name string) string {
	c, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return c.Value
}

// Authenticator is what the guard asks who is calling. Every dependency is a
// value, so a test builds one without the application.
type Authenticator struct {
	Store   *Store
	Keys    *Keys
	Pass    Passwords
	Limiter *Limiter
	// Secret returns the session secret. It is a getter because the file the
	// secret lives in is created by the data runner, which starts after every
	// provider registered; an empty result denies every session.
	Secret func() []byte
	// TrustLoopback is SERVER_TRUST_LOOPBACK: whether a request from this machine
	// counts as local.
	TrustLoopback bool
	// ForceTLS is AUTH_COOKIE_SECURE.
	ForceTLS bool
	// OIDCUsable and SAMLUsable report whether a single sign-on type is
	// configured. The caller supplies them so this package needs no
	// identity-provider code.
	OIDCUsable func(issuer, clientID, clientSecret string) bool
	SAMLUsable func(entryPoint, cert, idpEntity string) bool
	// SSOAllow reports whether an address may sign in through a provider, and
	// whether only an exact match counts.
	SSOAllow func(email string, exactOnly bool) bool
	Now      func() time.Time
}

// NewAuthenticator builds the authenticator. hasher is the framework's bcrypt
// hasher, whose cost comes from config/hashing.go.
func NewAuthenticator(store *Store, keys *Keys, hasher frameworkhash.Hash, initial string, secret func() []byte, trustLoopback, forceTLS bool) *Authenticator {
	return &Authenticator{
		Store: store, Keys: keys, Pass: NewPasswords(hasher, initial),
		Limiter: NewLimiter(), Secret: secret, TrustLoopback: trustLoopback, ForceTLS: forceTLS, Now: time.Now,
	}
}

// Session reports whether a request carries a valid session cookie. The `pv`
// claim must still match, so a password change ends every session at once.
func (a *Authenticator) Session(r *nethttp.Request) bool {
	token := RequestCookie(r, CookieName)
	if token == "" {
		return false
	}
	claims, ok := Verify(a.Secret(), token, a.Now())
	if !ok {
		return false
	}
	st := a.Store.Get(r.Context())
	if !SamePV(claims.PV, PV(a.Secret(), st.Password, st.Epoch)) {
		return false
	}
	// A provider session lives only while its method is still in force and its
	// address is still allowed, so switching the mode off ends it at the next
	// request rather than at the next expiry.
	switch claims.Method {
	case MethodOIDC, MethodSAML:
		mode := a.Mode(r.Context())
		if (claims.Method == MethodOIDC && !mode.OIDC) || (claims.Method == MethodSAML && !mode.SAML) {
			return false
		}
		return a.AllowSSO(claims.Email, false)
	}
	return true
}

// Claims returns the session claims for a request that has one.
func (a *Authenticator) Claims(r *nethttp.Request) (Claims, bool) {
	claims, ok := Verify(a.Secret(), RequestCookie(r, CookieName), a.Now())
	if !ok {
		return Claims{}, false
	}
	return claims, true
}

// APIKey judges the key a client request presents.
func (a *Authenticator) APIKey(r *nethttp.Request) identity.KeyResult {
	key := identity.ExtractKey(r)
	if key == "" {
		return identity.KeyResult{Status: identity.KeyNone}
	}
	return a.Keys.Lookup(r.Context(), key)
}

// RequireLogin reports whether the dashboard demands a login.
func (a *Authenticator) RequireLogin(ctx context.Context) bool {
	return a.Store.Get(ctx).RequireLogin
}

// RequireAPIKey reports whether a local client API call needs a valid key.
func (a *Authenticator) RequireAPIKey(ctx context.Context) bool {
	return a.Store.Get(ctx).RequireAPIKey
}

// Mode resolves the sign-in modes in force. SAML is not built yet, so only OIDC
// can be usable; a mode that names SAML therefore keeps the password form.
func (a *Authenticator) Mode(ctx context.Context) Mode {
	st := a.Store.Get(ctx)
	oidcUsable, samlUsable := false, false
	if a.OIDCUsable != nil {
		oidcUsable = a.OIDCUsable(st.OIDCIssuer, st.OIDCClientID, st.OIDCClientSecret)
	}
	if a.SAMLUsable != nil {
		samlUsable = a.SAMLUsable(st.SAMLEntryPoint, st.SAMLCert, st.SAMLIdPEntity)
	}
	return ResolveMode(st.AuthMode, st.SSOType, oidcUsable, samlUsable)
}

// AllowSSO reports whether an address may sign in through a provider. exactOnly
// is set when the address came from a claim the user can edit.
func (a *Authenticator) AllowSSO(email string, exactOnly bool) bool {
	if a.SSOAllow == nil {
		return false
	}
	return a.SSOAllow(email, exactOnly)
}

// IssueSSO signs a session started through a provider, and refuses when that
// method is no longer in force or the address is no longer allowed.
func (a *Authenticator) IssueSSO(ctx context.Context, method, email, name string, exactOnly, secure bool) (contractshttp.Cookie, bool) {
	mode := a.Mode(ctx)
	if (method == MethodOIDC && !mode.OIDC) || (method == MethodSAML && !mode.SAML) || method == MethodPassword {
		return contractshttp.Cookie{}, false
	}
	if !a.AllowSSO(email, exactOnly) {
		return contractshttp.Cookie{}, false
	}
	return a.Issue(ctx, method, email, name, secure)
}

// Issue signs a session for a caller who just proved who they are. secure comes
// from the request: TLS here, or a trusted proxy that said the caller used it.
func (a *Authenticator) Issue(ctx context.Context, method, email, name string, secure bool) (contractshttp.Cookie, bool) {
	st := a.Store.Get(ctx)
	claims := Claims{
		Authenticated: true, Method: method, Email: email, Name: name,
		PV: PV(a.Secret(), st.Password, st.Epoch),
	}
	token, err := Sign(a.Secret(), claims)
	if err != nil {
		return contractshttp.Cookie{}, false
	}
	return contractshttp.Cookie{
		Name: CookieName, Value: token, Path: "/", MaxAge: int(SessionTTL.Seconds()),
		Secure: a.ForceTLS || secure, HttpOnly: true, SameSite: "lax",
	}, true
}

// ClearedCookie is the cookie that ends a session in the browser.
func ClearedCookie() contractshttp.Cookie {
	return contractshttp.Cookie{Name: CookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: "lax"}
}
