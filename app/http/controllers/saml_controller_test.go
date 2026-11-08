package controllers

import (
	"context"
	"encoding/json"
	nethttp "net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"rllmx/app/auth"
	apphttp "rllmx/app/http"
	"rllmx/app/identity"
	"rllmx/app/repository"
	"rllmx/app/saml"
	"rllmx/app/settings"
	"rllmx/app/sso"
	"rllmx/tests"
	"rllmx/tests/store"
)

// samlRig is the SAML stack over a real store and a signing identity provider.
type samlRig struct {
	route   nethttp.Handler
	auth    *auth.Authenticator
	ctrl    *SAMLController
	sq      *store.Store
	idp     *samlTestIDP
	baseURL string
}

// newSAMLRig wires the three routes with a provider whose certificate and entry
// point are already in the settings row.
func newSAMLRig(t *testing.T) *samlRig {
	t.Helper()
	p := newSAMLTestIDP(t)
	sq := store.Migrated(t)
	ctx := context.Background()

	if err := sq.Settings().Patch(ctx, map[string]string{
		"authMode":           `"saml"`,
		"samlEntryPoint":     `"` + p.entryPoint + `"`,
		"samlCert":           quoteJSON(p.certPEM),
		"samlIdpEntityId":    `"` + p.entityID + `"`,
		"samlAttributeEmail": `"mail"`,
		"samlAttributeName":  `"cn"`,
	}); err != nil {
		t.Fatal(err)
	}

	db := func() *repository.Store { return sq.Store }
	codec := auth.NewKeys(db, nil, identity.NewKeys(""))
	secret := []byte("0123456789abcdef0123456789abcdef")
	a := auth.NewAuthenticator(auth.NewStore(db), codec, bcryptOnly{}, "", func() []byte { return secret }, true, false)
	a.OIDCUsable = func(string, string, string) bool { return false }
	a.SAMLUsable = func(entryPoint, cert, idpEntity string) bool {
		s := saml.Settings{EntryPoint: entryPoint, Cert: cert, IdPEntityID: idpEntity}
		if !s.Configured() || idpEntity == "" {
			return false
		}
		_, err := s.Validate()
		return err == nil
	}
	allowSSO := sso.ParseAllow("")
	a.SSOAllow = allowSSO.Allows

	server, err := settings.LoadServer(tests.Config{Values: tests.ServerValues()})
	if err != nil {
		t.Fatal(err)
	}
	ctrl := &SAMLController{
		Auth: a, Trust: apphttp.NewTrust(nil), Server: server,
		Log: &tests.Log{}, DB: db,
		Pending: sso.NewPending(), Replay: saml.NewReplay(), Allow: allowSSO,
	}

	r := tests.NewRoute(t)
	g := apphttp.NewGuard(a, true)
	r.SetGlobalMiddleware(nil)
	g.Handle(r, apphttp.ClassPublic, nethttp.MethodGet, "/api/auth/saml/start", ctrl.Start)
	g.Handle(r, apphttp.ClassPublic, nethttp.MethodPost, "/api/auth/saml/acs", ctrl.ACS)
	g.Handle(r, apphttp.ClassPublic, nethttp.MethodGet, "/api/auth/saml/metadata", ctrl.Metadata)
	return &samlRig{route: r, auth: a, ctrl: ctrl, sq: sq, idp: p, baseURL: "http://localhost:20128"}
}

func (r *samlRig) get(path string, hdr map[string]string) *httptest.ResponseRecorder {
	return r.do(nethttp.MethodGet, path, "", hdr)
}

func (r *samlRig) do(method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	var req *nethttp.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	req.RemoteAddr, req.Host = localPeer, localHost
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	r.route.ServeHTTP(rec, req)
	return rec
}

func TestSAMLStartSendsTheBrowserToTheEntryPoint(t *testing.T) {
	r := newSAMLRig(t)
	rec := r.get("/api/auth/saml/start", nil)
	if rec.Code != nethttp.StatusFound {
		t.Fatalf("%d %q", rec.Code, rec.Body.String())
	}
	loc := location(rec)
	if !strings.HasPrefix(loc, r.idp.entryPoint+"?") {
		t.Fatalf("location %q", loc)
	}
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatal(err)
	}
	if u.Query().Get("SAMLRequest") == "" || u.Query().Get("RelayState") == "" {
		t.Fatalf("location %q", loc)
	}
	var bind *nethttp.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == SAMLBindCookie {
			bind = c
		}
	}
	if bind == nil || bind.Value == "" || bind.Path != "/api/auth/saml" {
		t.Fatalf("bind cookie %+v", bind)
	}
}

func TestSAMLStartRefusesWithoutAProvider(t *testing.T) {
	r := newSAMLRig(t)
	if err := r.sq.Settings().Patch(context.Background(), map[string]string{"samlCert": `""`}); err != nil {
		t.Fatal(err)
	}
	r.auth.Store.Changed()
	rec := r.get("/api/auth/saml/start", nil)
	if rec.Code != nethttp.StatusFound || !strings.HasSuffix(location(rec), "error="+sso.CodeDisabled) {
		t.Fatalf("%d %q", rec.Code, location(rec))
	}
}

func TestSAMLMetadataServesTheDocument(t *testing.T) {
	r := newSAMLRig(t)
	rec := r.get("/api/auth/saml/metadata", nil)
	if rec.Code != nethttp.StatusOK {
		t.Fatalf("%d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/xml") {
		t.Fatalf("content type %q", ct)
	}
	body := rec.Body.String()
	want := "http://localhost:20128/api/auth/saml/metadata"
	if !strings.Contains(body, want) {
		t.Fatalf("the entity id %q is missing from %s", want, body)
	}
	if !strings.Contains(body, "http://localhost:20128/api/auth/saml/acs") {
		t.Fatalf("the ACS is missing from %s", body)
	}
}

func TestSAMLMetadataIsAbsentWhenSAMLIsOff(t *testing.T) {
	r := newSAMLRig(t)
	if err := r.sq.Settings().Patch(context.Background(), map[string]string{"authMode": `"password"`}); err != nil {
		t.Fatal(err)
	}
	r.auth.Store.Changed()
	if rec := r.get("/api/auth/saml/metadata", nil); rec.Code != nethttp.StatusNotFound {
		t.Fatalf("%d: the document is served while SAML is off", rec.Code)
	}
}

func TestTheACSRefusesAnEmptyBody(t *testing.T) {
	r := newSAMLRig(t)
	rec := r.do(nethttp.MethodPost, "/api/auth/saml/acs", "RelayState=x", nil)
	if rec.Code != nethttp.StatusFound || !strings.HasSuffix(location(rec), "error="+sso.CodeInvalid) {
		t.Fatalf("%d %q", rec.Code, location(rec))
	}
}

func TestTheACSRefusesAnArtifactBinding(t *testing.T) {
	// The artifact binding needs an outbound SOAP call, which is not built, so a
	// request carrying an artifact is refused even when the rest would verify.
	r := newSAMLRig(t)
	start := r.get("/api/auth/saml/start", nil)
	relay := relayOf(location(start))
	body := url.Values{
		"SAMLResponse": {r.idp.response(t, relay, r.baseURL)},
		"SAMLart":      {"an-artifact"},
		"RelayState":   {relay},
	}.Encode()
	hdr := map[string]string{"Cookie": SAMLBindCookie + "=" + sso.BindFor(r.auth.Secret(), SAMLBindCookie, relay)}
	rec := r.do(nethttp.MethodPost, "/api/auth/saml/acs", body, hdr)
	if sessionCookie(rec) != "" {
		t.Fatal("an artifact binding produced a session")
	}
	if !strings.HasSuffix(location(rec), "error="+sso.CodeInvalid) {
		t.Fatalf("%q", location(rec))
	}
}

func TestTheACSRefusesAMissingBindCookie(t *testing.T) {
	r := newSAMLRig(t)
	start := r.get("/api/auth/saml/start", nil)
	relay := relayOf(location(start))
	// A valid, signed response: the bind cookie is then the only thing standing
	// between the callback and a session.
	body := url.Values{
		"SAMLResponse": {r.idp.response(t, relay, r.baseURL)},
		"RelayState":   {relay},
	}.Encode()
	rec := r.do(nethttp.MethodPost, "/api/auth/saml/acs", body, nil)
	if rec.Code != nethttp.StatusFound || !strings.HasSuffix(location(rec), "error="+sso.CodeInvalid) {
		t.Fatalf("%d %q", rec.Code, location(rec))
	}
	if sessionCookie(rec) != "" {
		t.Fatal("a callback without the bind cookie issued a session")
	}
}

func TestASuccessfulSignInIssuesTheSession(t *testing.T) {
	r := newSAMLRig(t)
	rec, _ := r.flow(t, nil)
	if rec.Code != nethttp.StatusFound || location(rec) != "http://localhost:20128/" {
		t.Fatalf("%d %q", rec.Code, location(rec))
	}
	cookie := sessionCookie(rec)
	if cookie == "" {
		t.Fatal("no session cookie")
	}
	req := httptest.NewRequest(nethttp.MethodGet, "/", nil)
	req.AddCookie(&nethttp.Cookie{Name: auth.CookieName, Value: cookie})
	claims, ok := r.auth.Claims(req)
	if !ok || claims.Method != auth.MethodSAML || claims.Email != "ops@example.com" || claims.Name != "Ops" {
		t.Fatalf("%+v ok=%v", claims, ok)
	}
	if !r.auth.Session(req) {
		t.Fatal("a fresh SAML session does not validate")
	}
}

func TestASecondUseOfTheSameAssertionIsRefused(t *testing.T) {
	r := newSAMLRig(t)
	rec, cookies := r.flow(t, nil)
	if sessionCookie(rec) == "" {
		t.Fatal("the first sign-in failed")
	}
	start := r.get("/api/auth/saml/start", nil)
	relay := relayOf(location(start))
	// The same response body, replayed under a fresh relay state and cookie.
	body := url.Values{
		"SAMLResponse": {r.idp.lastResponse},
		"RelayState":   {relay},
	}.Encode()
	hdr := map[string]string{"Cookie": SAMLBindCookie + "=" + sso.BindFor(r.auth.Secret(), SAMLBindCookie, relay)}
	again := r.do(nethttp.MethodPost, "/api/auth/saml/acs", body, hdr)
	if sessionCookie(again) != "" {
		t.Fatal("a replayed assertion issued a second session")
	}
	_ = cookies
}

func TestASAMLAddressOutsideTheAllowListIsRefused(t *testing.T) {
	r := newSAMLRig(t)
	r.ctrl.Allow = sso.ParseAllow("@other.example")
	rec, _ := r.flow(t, nil)
	if !strings.HasSuffix(location(rec), "error="+sso.CodeNotAllowed) {
		t.Fatalf("%q", location(rec))
	}
	if sessionCookie(rec) != "" {
		t.Fatal("a session was issued for an address outside the list")
	}
}

func TestASAMLFailureCountsUnderItsOwnPrefix(t *testing.T) {
	r := newSAMLRig(t)
	for range auth.FreeAttempts {
		r.do(nethttp.MethodPost, "/api/auth/saml/acs", "SAMLResponse=x&RelayState=y", nil)
	}
	if r.auth.Limiter.Locked("sso:"+auth.Key(localPeer), time.Now()) == 0 {
		t.Fatal("the failed callbacks did not count")
	}
	if r.auth.Limiter.Locked(auth.Key(localPeer), time.Now()) != 0 {
		t.Fatal("a provider failure spent the password budget")
	}
}

// flow runs start, then posts the signed response with the bind cookie, and
// returns the ACS answer.
func (r *samlRig) flow(t *testing.T, hdr map[string]string) (*httptest.ResponseRecorder, map[string]string) {
	t.Helper()
	start := r.get("/api/auth/saml/start", nil)
	if start.Code != nethttp.StatusFound || strings.Contains(location(start), "error=") {
		t.Fatalf("start: %d %q", start.Code, location(start))
	}
	relay := relayOf(location(start))
	body := url.Values{
		"SAMLResponse": {r.idp.response(t, relay, r.baseURL)},
		"RelayState":   {relay},
	}.Encode()
	cookies := map[string]string{"Cookie": SAMLBindCookie + "=" + sso.BindFor(r.auth.Secret(), SAMLBindCookie, relay)}
	for k, v := range hdr {
		cookies[k] = v
	}
	return r.do(nethttp.MethodPost, "/api/auth/saml/acs", body, cookies), cookies
}

// quoteJSON renders a string as a JSON literal, for a PEM block with newlines.
func quoteJSON(v string) string {
	return string(mustMarshal(v))
}

func mustMarshal(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// relayOf reads RelayState from the redirect.
func relayOf(loc string) string {
	u, err := url.Parse(loc)
	if err != nil {
		return ""
	}
	return u.Query().Get("RelayState")
}
