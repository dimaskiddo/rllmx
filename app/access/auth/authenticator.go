package auth

import (
	"context"
	"errors"
	nethttp "net/http"
	"strings"
	"time"
	"unicode/utf8"

	frameworkhash "github.com/goravel/framework/contracts/hash"
	contractshttp "github.com/goravel/framework/contracts/http"

	"rllmx/app/access/identity"
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

// Session is who a dashboard request acts as.
type Session struct {
	Role   string
	UserID string
	Name   string
	Email  string
	Method string
}

// Admin reports whether the session may configure the router.
func (s Session) Admin() bool { return s.Role != RoleUser }

// Authenticator is what the guard asks who is calling. Every dependency is a
// value, so a test builds one without the application.
type Authenticator struct {
	Store *Store
	// Users holds the standard-user accounts. Nil leaves the dashboard with the
	// one admin it had before users existed.
	Users   *Users
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
	// SSOAdmin reports whether an address signs in through a provider as the
	// administrator. A listed address is the admin; every other address the
	// provider authenticated is a standard user.
	SSOAdmin func(email string, exactOnly bool) bool
	// AdminName is AUTH_ADMIN_USERNAME. Empty means the built-in name.
	AdminName string
	// ClientAddr names the caller for the key limiter. Nil means the socket peer.
	ClientAddr func(r *nethttp.Request) string
	// SessionTTL is AUTH_SESSION_TTL: how long a session lasts. Zero means the
	// built-in 24 hours.
	SessionTTL time.Duration
	Now        func() time.Time
}

// NewAuthenticator builds the authenticator. hasher is the framework's bcrypt
// hasher, whose cost comes from config/hashing.go.
func NewAuthenticator(store *Store, keys *Keys, hasher frameworkhash.Hash, initial string, secret func() []byte, trustLoopback, forceTLS bool) *Authenticator {
	return &Authenticator{
		Store: store, Keys: keys, Pass: NewPasswords(hasher, initial),
		Limiter: NewLimiter(), Secret: secret, TrustLoopback: trustLoopback, ForceTLS: forceTLS, Now: time.Now,
	}
}

// Session reports whether a request carries a valid session cookie.
func (a *Authenticator) Session(r *nethttp.Request) bool {
	_, ok := a.SessionInfo(r)
	return ok
}

// SessionInfo names the signed-in caller and their role. The `pv` claim must
// still match, so a password change ends the sessions it applied to; a standard
// user is read from the user list on every request, so deleting or demoting
// them ends their session at once.
func (a *Authenticator) SessionInfo(r *nethttp.Request) (Session, bool) {
	token := RequestCookie(r, CookieName)
	if token == "" {
		return Session{}, false
	}
	claims, ok := Verify(a.Secret(), token, a.Now())
	if !ok {
		return Session{}, false
	}
	st := a.Store.Get(r.Context())
	if claims.UID != "" {
		return a.userSession(r, claims, st.Epoch)
	}
	if !SamePV(claims.PV, PV(a.Secret(), st.Password, st.Epoch)) {
		return Session{}, false
	}
	// A provider admin session lives only while its method is in force and its
	// address is still on the admin list, so switching the mode off or removing
	// the address ends it at the next request rather than at the next expiry.
	if !a.adminSessionAllowed(r, claims) {
		return Session{}, false
	}
	return Session{Role: RoleAdmin, Name: claims.Name, Email: claims.Email, Method: claims.Method}, true
}

// userSession verifies a standard user's token against that user's own password
// hash and the epoch, so a reset ends only their sessions.
func (a *Authenticator) userSession(r *nethttp.Request, claims Claims, epoch string) (Session, bool) {
	if a.Users == nil {
		return Session{}, false
	}
	u, ok := a.Users.Get(r.Context(), claims.UID)
	if !ok {
		return Session{}, false
	}
	if !SamePV(claims.PV, PV(a.Secret(), u.Hash, epoch)) {
		return Session{}, false
	}
	// A standard user's session rests on the stored account, not on the admin
	// list, which decides the role, not admission.
	if !a.modeAllows(r, claims) {
		return Session{}, false
	}
	return Session{Role: RoleUser, UserID: u.ID, Name: u.Username, Email: u.Email, Method: claims.Method}, true
}

// adminSessionAllowed re-checks a provider session against the admin list; a
// password session has no address and is already proof on its own.
func (a *Authenticator) adminSessionAllowed(r *nethttp.Request, claims Claims) bool {
	switch claims.Method {
	case MethodOIDC, MethodSAML:
		return a.modeAllows(r, claims) && a.IsSSOAdmin(claims.Email, false)
	}
	return true
}

// modeAllows reports whether a provider session's method is still in force.
func (a *Authenticator) modeAllows(r *nethttp.Request, claims Claims) bool {
	mode := a.Mode(r.Context())
	switch claims.Method {
	case MethodOIDC:
		return mode.OIDC
	case MethodSAML:
		return mode.SAML
	}
	return true
}

// APIKey judges the key a client request presents.
func (a *Authenticator) APIKey(r *nethttp.Request) identity.KeyResult {
	key := identity.ExtractKey(r)
	if key == "" {
		return identity.KeyResult{Status: identity.KeyNone}
	}
	return a.Keys.Lookup(r.Context(), key)
}

// KeyLocked reports how long the caller of r must wait before another key is
// looked up. A caller that guessed wrong too often gets no answer, right or
// wrong, so the lock cannot be used to test a guess.
func (a *Authenticator) KeyLocked(r *nethttp.Request) time.Duration {
	return a.Limiter.Locked(a.keyLimit(r), a.Now())
}

// KeyFailed records one refused key against the caller of r. A valid key never
// clears the count: whoever holds one valid key could otherwise reset it while
// guessing for another.
func (a *Authenticator) KeyFailed(r *nethttp.Request) {
	a.Limiter.Fail(a.keyLimit(r), a.Now())
}

func (a *Authenticator) keyLimit(r *nethttp.Request) string {
	addr := r.RemoteAddr
	if a.ClientAddr != nil {
		addr = a.ClientAddr(r)
	}
	return "key:" + Key(addr)
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

// IsSSOAdmin reports whether an address signs in as the administrator through a
// provider. exactOnly is set when the address came from a claim the user can
// edit, so only an exact entry counts.
func (a *Authenticator) IsSSOAdmin(email string, exactOnly bool) bool {
	if a.SSOAdmin == nil {
		return false
	}
	return a.SSOAdmin(email, exactOnly)
}

// IssueSSO signs a session started through a provider. An address on the admin
// list is the admin; every other address is a standard user, provisioned on its
// first sign-in with a password nobody knows.
func (a *Authenticator) IssueSSO(ctx context.Context, method, email, name string, exactOnly, secure bool) (contractshttp.Cookie, bool) {
	mode := a.Mode(ctx)
	if (method == MethodOIDC && !mode.OIDC) || (method == MethodSAML && !mode.SAML) || method == MethodPassword {
		return contractshttp.Cookie{}, false
	}
	if a.IsSSOAdmin(email, exactOnly) {
		return a.Issue(ctx, method, email, name, secure)
	}
	u, ok := a.ssoUser(ctx, email, exactOnly)
	if !ok {
		return contractshttp.Cookie{}, false
	}
	return a.IssueUser(ctx, u, method, secure)
}

// ssoUser is the standard user an address signs in as. An address from a
// user-editable claim signs in as nobody: it is not proof of the address, so
// matching it to an account would hand that account to whoever typed it.
func (a *Authenticator) ssoUser(ctx context.Context, email string, exactOnly bool) (User, bool) {
	if a.Users == nil || exactOnly || strings.TrimSpace(email) == "" {
		return User{}, false
	}
	if u, ok := a.Users.ByEmail(ctx, email); ok {
		return u, true
	}
	secret, err := NewUserPassword()
	if err != nil {
		return User{}, false
	}
	hash, err := a.Pass.Hash(secret)
	if err != nil {
		return User{}, false
	}
	provision := func(derived bool) error {
		_, err := a.Users.Add(ctx, func(id string) User {
			return User{ID: id, Username: ssoUsername(email, id, derived), Email: email, Hash: hash, Provisioned: true}
		})
		return err
	}
	if err := provision(false); err != nil {
		if !errors.Is(err, ErrUserTaken) {
			return User{}, false
		}
		// A password user already holds the address as a name. The address is
		// the account's identity, so it gets a derived name, not that user.
		if err := provision(true); err != nil {
			return User{}, false
		}
	}
	if stored, ok := a.Users.ByEmail(ctx, email); ok {
		return stored, true
	}
	return User{}, false
}

// ssoUsername is the name a provisioned account carries: the address when it
// fits a username, else its start, and with the id's tail when the plain name
// is taken or the address is too long to keep whole.
func ssoUsername(email, id string, disambiguate bool) string {
	name := strings.ToLower(strings.TrimSpace(email))
	if !disambiguate && len(name) <= maxUsername {
		return name
	}
	tail := id
	if len(tail) > 8 {
		tail = tail[len(tail)-8:]
	}
	end := min(len(name), maxUsername-len(tail)-1)
	for end > 0 && end < len(name) && !utf8.RuneStart(name[end]) {
		end--
	}
	return name[:end] + "~" + tail
}

// Admin reports the username the administrator signs in with.
func (a *Authenticator) Admin() string {
	return AdminName(a.AdminName)
}

// Issue signs a session for the admin, who just proved the operator's password.
// secure comes from the request: TLS here, or a trusted proxy that said so.
func (a *Authenticator) Issue(ctx context.Context, method, email, name string, secure bool) (contractshttp.Cookie, bool) {
	st := a.Store.Get(ctx)
	claims := Claims{
		Authenticated: true, Method: method, Email: email, Name: name,
		PV: PV(a.Secret(), st.Password, st.Epoch),
	}
	return a.issue(claims, secure)
}

// IssueUser signs a session for a standard user, tied to that user's own
// password hash so a reset ends their sessions and nobody else's.
func (a *Authenticator) IssueUser(ctx context.Context, u User, method string, secure bool) (contractshttp.Cookie, bool) {
	st := a.Store.Get(ctx)
	claims := Claims{
		Authenticated: true, Method: method, UID: u.ID, Email: u.Email, Name: u.Username,
		PV: PV(a.Secret(), u.Hash, st.Epoch),
	}
	return a.issue(claims, secure)
}

func (a *Authenticator) issue(claims Claims, secure bool) (contractshttp.Cookie, bool) {
	ttl := a.SessionTTL
	if ttl <= 0 {
		ttl = SessionTTL
	}
	token, err := Sign(a.Secret(), claims, ttl)
	if err != nil {
		return contractshttp.Cookie{}, false
	}
	return contractshttp.Cookie{
		Name: CookieName, Value: token, Path: "/", MaxAge: int(ttl.Seconds()),
		Secure: a.ForceTLS || secure, HttpOnly: true, SameSite: "lax",
	}, true
}

// ClearedCookie is the cookie that ends a session in the browser.
func ClearedCookie() contractshttp.Cookie {
	return contractshttp.Cookie{Name: CookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: "lax"}
}
