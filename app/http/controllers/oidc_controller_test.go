package controllers

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	nethttp "net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"rllmx/app/auth"
	"rllmx/app/cache"
	apphttp "rllmx/app/http"
	"rllmx/app/identity"
	"rllmx/app/network"
	"rllmx/app/oidc"
	"rllmx/app/repository"
	"rllmx/app/settings"
	"rllmx/app/sso"
	"rllmx/tests"
	"rllmx/tests/store"
)

// idp is a stand-in provider: discovery, JWKS and a token endpoint that answers
// with a token this test signed.
type idp struct {
	server *httptest.Server
	issuer string
	key    jose.JSONWebKey
	signer jose.Signer
	// claims the token endpoint puts in the next ID token.
	claims map[string]any
	// issue, when set, signs the token itself so a test can tamper with it.
	issue func(t *testing.T, claims map[string]any) string
	hits  int
}

func newIDP(t *testing.T) *idp {
	t.Helper()
	priv := mustRSAKey(t)
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: priv}, (&jose.SignerOptions{}).WithHeader("kid", "k1").WithType("JWT"))
	if err != nil {
		t.Fatal(err)
	}
	p := &idp{
		key:    jose.JSONWebKey{Key: &priv.PublicKey, KeyID: "k1", Algorithm: string(jose.RS256), Use: "sig"},
		signer: signer,
	}
	p.server = httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		p.hits++
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			writeJSON(w, map[string]any{
				"issuer":                 p.issuer,
				"authorization_endpoint": p.issuer + "/authorize",
				"token_endpoint":         p.server.URL + "/token",
				"jwks_uri":               p.server.URL + "/jwks",
			})
		case "/jwks":
			writeJSON(w, map[string]any{"keys": []jose.JSONWebKey{p.key}})
		case "/token":
			if err := r.ParseForm(); err != nil {
				w.WriteHeader(nethttp.StatusBadRequest)
				return
			}
			token := p.sign(t, p.claims)
			writeJSON(w, map[string]any{"id_token": token, "access_token": "an-access-token", "token_type": "Bearer"})
		default:
			w.WriteHeader(nethttp.StatusNotFound)
		}
	}))
	p.issuer = p.server.URL
	t.Cleanup(p.server.Close)
	return p
}

func (p *idp) sign(t *testing.T, claims map[string]any) string {
	if p.issue != nil {
		return p.issue(t, claims)
	}
	return p.signWith(t, claims)
}

func writeJSON(w nethttp.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// oidcRig is the real stack the server installs, over a real store and a
// stand-in provider.
type oidcRig struct {
	route  nethttp.Handler
	guard  *apphttp.Guard
	auth   *auth.Authenticator
	client *sso.Client
	ctrl   *OIDCController
	sq     *store.Store
	idp    *idp
}

func newOIDCRig(t *testing.T, configure func(*idp)) *oidcRig {
	t.Helper()
	p := newIDP(t)
	if configure != nil {
		configure(p)
	}
	sq := store.Migrated(t)
	ctx := contextBackground()
	allow, err := network.ParseAllow("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}

	// The DB closure returns the real store, whose settings row points at the
	// stand-in provider's loopback address.
	if err := sq.Settings().Patch(ctx, map[string]string{
		"authMode":         `"oidc"`,
		"oidcIssuerUrl":    `"` + p.issuer + `"`,
		"oidcClientId":     `"client-abc"`,
		"oidcClientSecret": `"a-secret"`,
		"oidcScopes":       `"openid profile email"`,
	}); err != nil {
		t.Fatal(err)
	}

	db := func() *repository.Store { return sq.Store }
	codec := auth.NewKeys(db, nil, identity.NewKeys(""))
	secret := []byte("0123456789abcdef0123456789abcdef")
	a := auth.NewAuthenticator(auth.NewStore(db), codec, bcryptOnly{}, "", func() []byte { return secret }, true, false)
	a.OIDCUsable = func(issuer, id, s string) bool { return issuer != "" && id != "" && s != "" }
	allowSSO := sso.ParseAllow("")
	a.SSOAllow = allowSSO.Allows

	server, err := settings.LoadServer(tests.Config{Values: tests.ServerValues()})
	if err != nil {
		t.Fatal(err)
	}
	ctrl := &OIDCController{
		Auth: a, Trust: apphttp.NewTrust(nil), Server: server,
		Client:  sso.NewClient(allow),
		Cache:   cache.New(tests.NewCache(), cache.WithoutShared()),
		Log:     &tests.Log{},
		DB:      db,
		Pending: sso.NewPending(),
		Allow:   allowSSO,
	}

	r := tests.NewRoute(t)
	g := apphttp.NewGuard(a, true)
	r.SetGlobalMiddleware(nil)
	g.Handle(r, apphttp.ClassPublic, nethttp.MethodGet, "/api/auth/oidc/start", ctrl.Start)
	g.Handle(r, apphttp.ClassPublic, nethttp.MethodGet, "/api/auth/oidc/callback", ctrl.Callback)
	return &oidcRig{route: r, guard: g, auth: a, sq: sq, client: ctrl.Client, ctrl: ctrl, idp: p}
}

func (r *oidcRig) get(path string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(nethttp.MethodGet, path, nil)
	req.RemoteAddr, req.Host = localPeer, localHost
	req.Header.Set("Host", localHost)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	r.route.ServeHTTP(rec, req)
	return rec
}

func location(rec *httptest.ResponseRecorder) string { return rec.Header().Get("Location") }

func TestStartSendsTheBrowserToTheProvider(t *testing.T) {
	r := newOIDCRig(t, nil)
	rec := r.get("/api/auth/oidc/start", nil)
	if rec.Code != nethttp.StatusFound {
		t.Fatalf("%d %q", rec.Code, rec.Body.String())
	}
	if loc := location(rec); strings.Contains(loc, "error=") {
		doc, derr := oidc.Discover(contextBackground(), r.client, cachedFor(), r.idp.issuer)
		t.Fatalf("start failed: %q; discovery %+v err %v", loc, doc, derr)
	}
	loc := location(rec)
	if !strings.HasPrefix(loc, r.idp.issuer+"/authorize?") {
		t.Fatalf("location %q", loc)
	}
	for _, want := range []string{"response_type=code", "client_id=client-abc", "code_challenge_method=S256", "state=", "nonce=", "scope="} {
		if !strings.Contains(loc, want) {
			t.Errorf("%q is missing from %q", want, loc)
		}
	}
	var bind *nethttp.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == sso.BindCookie {
			bind = c
		}
	}
	if bind == nil || bind.Value == "" || bind.Path != "/api/auth/oidc" || !bind.HttpOnly {
		t.Fatalf("bind cookie %+v", bind)
	}
}

func TestStartRefusesWithoutAProvider(t *testing.T) {
	r := newOIDCRig(t, nil)
	if err := r.sq.Settings().Patch(contextBackground(), map[string]string{"oidcIssuerUrl": `""`}); err != nil {
		t.Fatal(err)
	}
	r.auth.Store.Changed()
	rec := r.get("/api/auth/oidc/start", nil)
	if rec.Code != nethttp.StatusFound || !strings.HasSuffix(location(rec), "error="+sso.CodeDisabled) {
		t.Fatalf("%d %q", rec.Code, location(rec))
	}
}

func TestCallbackWithoutStateIsRefused(t *testing.T) {
	r := newOIDCRig(t, nil)
	rec := r.get("/api/auth/oidc/callback?code=x", nil)
	if rec.Code != nethttp.StatusFound || !strings.HasSuffix(location(rec), "error="+sso.CodeInvalid) {
		t.Fatalf("%d %q", rec.Code, location(rec))
	}
}

func TestCallbackWithoutTheBindCookieIsRefused(t *testing.T) {
	r := newOIDCRig(t, nil)
	start := r.get("/api/auth/oidc/start", nil)
	state := queryParam(location(start), "state")
	// The token the provider would answer with is valid for this flow, so the
	// bind cookie is the only thing standing between the callback and a session.
	r.idp.claims = claimsFor(t, r.idp.issuer, queryParam(location(start), "nonce"))

	rec := r.get("/api/auth/oidc/callback?code=an-auth-code&state="+state, nil)
	if sessionCookie(rec) != "" {
		t.Fatal("a callback without the bind cookie issued a session: the state alone is not proof of the browser")
	}
	if !strings.HasSuffix(location(rec), "error="+sso.CodeInvalid) {
		t.Fatalf("%q", location(rec))
	}
}

func TestCallbackWithAnUnknownStateIsRefused(t *testing.T) {
	r := newOIDCRig(t, nil)
	rec := r.get("/api/auth/oidc/callback?code=x&state=unknown", map[string]string{
		"Cookie": sso.BindCookie + "=" + sso.Bind(r.auth.Secret(), "unknown"),
	})
	if rec.Code != nethttp.StatusFound || !strings.HasSuffix(location(rec), "error="+sso.CodeExpired) {
		t.Fatalf("%d %q", rec.Code, location(rec))
	}
}

func TestAProviderErrorIsPassedThroughAsOneCode(t *testing.T) {
	r := newOIDCRig(t, nil)
	rec := r.get("/api/auth/oidc/callback?error=access_denied&error_description=provider-detail-that-must-not-leak", nil)
	if rec.Code != nethttp.StatusFound {
		t.Fatalf("%d", rec.Code)
	}
	if strings.Contains(location(rec), "provider-detail") {
		t.Fatalf("the location repeats provider text: %q", location(rec))
	}
	if !strings.HasSuffix(location(rec), "error="+sso.CodeIdPError) {
		t.Fatalf("location %q", location(rec))
	}
}

func queryParam(target, name string) string {
	u, err := url.Parse(target)
	if err != nil {
		return ""
	}
	return u.Query().Get(name)
}

func mustRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return priv
}

// contextBackground is the context a settings write takes.
func contextBackground() context.Context { return context.Background() }

func cachedFor() *cache.Cache { return cache.New(tests.NewCache(), cache.WithoutShared()) }

// flow walks start and callback with the bind cookie the start set, so a test
// can assert on the finished sign-in.
func (r *oidcRig) flow(t *testing.T, hdr map[string]string) (*httptest.ResponseRecorder, map[string]string) {
	t.Helper()
	start := r.get("/api/auth/oidc/start", nil)
	if start.Code != nethttp.StatusFound || strings.Contains(location(start), "error=") {
		t.Fatalf("start: %d %q", start.Code, location(start))
	}
	state := queryParam(location(start), "state")
	// The provider echoes the nonce the client sent, which is what a real one does.
	r.idp.claims = claimsFor(t, r.idp.issuer, queryParam(location(start), "nonce"))
	cookies := map[string]string{"Cookie": sso.BindCookie + "=" + bindOf(start)}
	for k, v := range hdr {
		cookies[k] = v
	}
	return r.get("/api/auth/oidc/callback?code=an-auth-code&state="+state, cookies), cookies
}

// bindOf returns the bind cookie the start set.
func bindOf(rec *httptest.ResponseRecorder) string {
	for _, c := range rec.Result().Cookies() {
		if c.Name == sso.BindCookie {
			return c.Value
		}
	}
	return ""
}

// sessionCookie returns the dashboard cookie a finished sign-in set.
func sessionCookie(rec *httptest.ResponseRecorder) string {
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.CookieName && c.Value != "" {
			return c.Value
		}
	}
	return ""
}

func claimsFor(t *testing.T, issuer, nonce string) map[string]any {
	t.Helper()
	return map[string]any{
		"iss": issuer, "sub": "user-1", "aud": "client-abc",
		"exp": time.Now().Add(5 * time.Minute).Unix(), "iat": time.Now().Unix(),
		"nonce": nonce, "email": "ops@example.com", "email_verified": true, "name": "Ops",
	}
}

func TestASuccessfulSignInIssuesTheSessionCookie(t *testing.T) {
	r := newOIDCRig(t, nil)
	rec, _ := r.flow(t, nil)
	if rec.Code != nethttp.StatusFound || location(rec) != "http://localhost:20128/" {
		t.Fatalf("%d %q", rec.Code, location(rec))
	}
	if sessionCookie(rec) == "" {
		t.Fatal("no session cookie was issued")
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == sso.BindCookie && c.MaxAge >= 0 && c.Value != "" {
			t.Fatal("the bind cookie was not cleared")
		}
	}
}

func TestTheSessionFromAProviderCarriesItsMethod(t *testing.T) {
	r := newOIDCRig(t, nil)
	rec, _ := r.flow(t, nil)
	cookie := sessionCookie(rec)
	if cookie == "" {
		t.Fatal("no session cookie")
	}
	req := httptest.NewRequest(nethttp.MethodGet, "/", nil)
	req.AddCookie(&nethttp.Cookie{Name: auth.CookieName, Value: cookie})
	claims, ok := r.auth.Claims(req)
	if !ok || claims.Method != auth.MethodOIDC || claims.Email != "ops@example.com" || claims.Name != "Ops" {
		t.Fatalf("%+v ok=%v", claims, ok)
	}
	if !r.auth.Session(req) {
		t.Fatal("a fresh provider session does not validate")
	}
}

func TestAnAddressOutsideTheAllowListIsRefused(t *testing.T) {
	r := newOIDCRig(t, nil)
	r.ctrl.Allow = sso.ParseAllow("@other.example")
	rec, _ := r.flow(t, nil)
	if !strings.HasSuffix(location(rec), "error="+sso.CodeNotAllowed) {
		t.Fatalf("%q", location(rec))
	}
	if sessionCookie(rec) != "" {
		t.Fatal("a session was issued for an address outside the list")
	}
}

func TestAnUnverifiedAddressIsRefused(t *testing.T) {
	r := newOIDCRig(t, nil)
	r.idp.issue = func(t *testing.T, claims map[string]any) string {
		claims["email_verified"] = false
		return r.idp.signWith(t, claims)
	}
	rec, _ := r.flow(t, nil)
	if !strings.HasSuffix(location(rec), "error="+sso.CodeUnverified) {
		t.Fatalf("%q", location(rec))
	}
}

func TestATokenForAnotherClientIsRefused(t *testing.T) {
	r := newOIDCRig(t, nil)
	r.idp.issue = func(t *testing.T, claims map[string]any) string {
		claims["aud"] = "another-client"
		return r.idp.signWith(t, claims)
	}
	rec, _ := r.flow(t, nil)
	if !strings.HasSuffix(location(rec), "error="+sso.CodeInvalid) {
		t.Fatalf("%q", location(rec))
	}
	if sessionCookie(rec) != "" {
		t.Fatal("a session was issued for a token minted for another client")
	}
}

func TestATokenWithoutAnAddressIsRefused(t *testing.T) {
	r := newOIDCRig(t, nil)
	r.idp.issue = func(t *testing.T, claims map[string]any) string {
		delete(claims, "email")
		delete(claims, "preferred_username")
		return r.idp.signWith(t, claims)
	}
	rec, _ := r.flow(t, nil)
	if !strings.HasSuffix(location(rec), "error="+sso.CodeNoEmail) {
		t.Fatalf("%q", location(rec))
	}
}

func TestAFailedCallbackCountsUnderItsOwnPrefix(t *testing.T) {
	r := newOIDCRig(t, nil)
	for range auth.FreeAttempts {
		r.get("/api/auth/oidc/callback?code=x&state=unknown", map[string]string{
			"Cookie": sso.BindCookie + "=" + sso.Bind(r.auth.Secret(), "unknown"),
		})
	}
	key := "sso:" + auth.Key(localPeer)
	if r.auth.Limiter.Locked(key, time.Now()) == 0 {
		t.Fatal("the failed callbacks did not count")
	}
	if r.auth.Limiter.Locked(auth.Key(localPeer), time.Now()) != 0 {
		t.Fatal("a provider failure spent the password budget")
	}
}

func TestSwitchingTheModeOffEndsAProviderSession(t *testing.T) {
	r := newOIDCRig(t, nil)
	rec, _ := r.flow(t, nil)
	req := httptest.NewRequest(nethttp.MethodGet, "/", nil)
	req.AddCookie(&nethttp.Cookie{Name: auth.CookieName, Value: sessionCookie(rec)})
	if !r.auth.Session(req) {
		t.Fatal("the session does not validate")
	}
	if err := r.sq.Settings().Patch(contextBackground(), map[string]string{"authMode": `"password"`}); err != nil {
		t.Fatal(err)
	}
	r.auth.Store.Changed()
	if r.auth.Session(req) {
		t.Fatal("a provider session outlived the mode that allowed it")
	}
}

// signWith signs the claims without the test's own hook, so an override can
// tamper with them and still get a real signature.
func (p *idp) signWith(t *testing.T, claims map[string]any) string {
	t.Helper()
	raw, err := jwt.Signed(p.signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestRemovingAnAddressFromTheListEndsItsSession(t *testing.T) {
	r := newOIDCRig(t, nil)
	rec, _ := r.flow(t, nil)
	cookie := sessionCookie(rec)
	if cookie == "" {
		t.Fatal("no session cookie")
	}
	req := httptest.NewRequest(nethttp.MethodGet, "/", nil)
	req.AddCookie(&nethttp.Cookie{Name: auth.CookieName, Value: cookie})
	if !r.auth.Session(req) {
		t.Fatal("the session does not validate")
	}
	// The address is now outside the list, so the next request ends the session
	// even though the cookie is unchanged.
	r.auth.SSOAllow = sso.ParseAllow("@other.example").Allows
	if r.auth.Session(req) {
		t.Fatal("a session outlived the allow list that admitted it")
	}
}
