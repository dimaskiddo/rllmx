package controllers

import (
	"context"
	"encoding/json"
	nethttp "net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	contractshttp "github.com/goravel/framework/contracts/http"

	"rllmx/app/auth"
	apphttp "rllmx/app/http"
	"rllmx/app/identity"
	"rllmx/app/oauth"
	"rllmx/app/repository"
	"rllmx/app/settings"
	"rllmx/app/upstream"
	"rllmx/tests"
	"rllmx/tests/store"
	"rllmx/web"
)

type oauthRig struct {
	auth   *auth.Authenticator
	route  nethttp.Handler
	cookie string
	svc    *oauth.Service
	seen   *[]string
	st     *store.Store
}

func newOAuthRig(t *testing.T, tokenBody string) *oauthRig {
	t.Helper()
	st := store.Migrated(t)
	db := func() *repository.Store { return st.Store }
	secret := []byte("0123456789abcdef0123456789abcdef")
	a := auth.NewAuthenticator(auth.NewStore(db), auth.NewKeys(db, nil, identity.NewKeys("")), bcryptOnly{}, "", func() []byte { return secret }, true, false)
	if err := st.Settings().Patch(context.Background(), map[string]string{"requireLogin": "true"}); err != nil {
		t.Fatal(err)
	}
	g := apphttp.NewGuard(a, true)

	var seen []string
	var mu sync.Mutex
	token := httptest.NewTLSServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		_ = r.ParseForm()
		mu.Lock()
		seen = append(seen, r.Form.Encode())
		mu.Unlock()
		_, _ = w.Write([]byte(tokenBody))
	}))
	t.Cleanup(token.Close)

	reg, err := upstream.Load(fstest.MapFS{"data/claude.json": {Data: []byte(`{"id":"claude","category":"oauth","transport":{"baseUrl":"https://x.example/v1","format":"claude"},"models":[],` +
		`"oauth":{"flow":"code-pkce","authorizeUrl":"https://a.example/authorize","tokenUrl":"` + token.URL + `","scope":"s","redirect":"local","exchange":"form","refresh":{"encoding":"json","leadSeconds":300}}}`)}}, "data")
	if err != nil {
		t.Fatal(err)
	}
	sessions := oauth.NewSessions()
	svc := &oauth.Service{
		Registry: reg, ClientID: func(string) string { return "cid" }, Sessions: sessions,
		Tokens: &oauth.TokenClient{HTTP: token.Client()}, Now: time.Now,
		Accounts: func() oauth.Accounts { return st.Connections() },
	}
	oc := &OAuthController{Service: svc, Auth: a, Server: &settings.Server{Port: 20128}}
	cb := &CallbackController{Files: web.Static}

	r := tests.NewRoute(t)
	r.SetGlobalMiddleware([]contractshttp.Middleware{guardMW{g}})
	handle := func(class apphttp.Class, method, path string, h contractshttp.HandlerFunc) {
		g.Handle(r, class, method, path, h)
	}
	handle(apphttp.ClassAlwaysProtected, nethttp.MethodGet, "/api/oauth/{provider}/authorize", oc.Authorize)
	handle(apphttp.ClassAlwaysProtected, nethttp.MethodPost, "/api/oauth/exchange", oc.Exchange)
	handle(apphttp.ClassAlwaysProtected, nethttp.MethodPost, "/api/oauth/callback", oc.Callback)
	handle(apphttp.ClassAlwaysProtected, nethttp.MethodGet, "/api/oauth/status", oc.Status)
	handle(apphttp.ClassPublic, nethttp.MethodGet, "/callback", cb.Page)
	handle(apphttp.ClassPublic, nethttp.MethodGet, "/static/js/callback.js", cb.Script)

	cookie, ok := a.Issue(context.Background(), auth.MethodPassword, "", "", false)
	if !ok {
		t.Fatal("no session")
	}
	return &oauthRig{auth: a, route: r, cookie: cookie.Value, svc: svc, seen: &seen, st: st}
}

func (o *oauthRig) call(method, path, body, cookie string) (int, map[string]any, string) {
	var req *nethttp.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	req.RemoteAddr, req.Host = localPeer, localHost
	if cookie != "" {
		req.AddCookie(&nethttp.Cookie{Name: auth.CookieName, Value: cookie})
	}
	rec := httptest.NewRecorder()
	o.route.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, rec.Body.String()
}

func (o *oauthRig) start(t *testing.T) map[string]any {
	t.Helper()
	code, out, raw := o.call("GET", "/api/oauth/claude/authorize", "", o.cookie)
	if code != 200 {
		t.Fatalf("authorize: %d %s", code, raw)
	}
	return out
}

func TestEveryLoginRouteNeedsASession(t *testing.T) {
	o := newOAuthRig(t, `{"access_token":"AT"}`)
	for _, rt := range [][2]string{
		{"GET", "/api/oauth/claude/authorize"}, {"POST", "/api/oauth/exchange"}, {"POST", "/api/oauth/callback"}, {"GET", "/api/oauth/status?state=x"},
	} {
		if code, _, _ := o.call(rt[0], rt[1], `{"state":"x","input":"y","code":"z"}`, ""); code != 401 {
			t.Errorf("%s %s without a session: %d", rt[0], rt[1], code)
		}
	}
}

func TestAuthorizeAnswersTheAddressAndNeverTheVerifier(t *testing.T) {
	o := newOAuthRig(t, `{"access_token":"AT"}`)
	out := o.start(t)
	state, _ := out["state"].(string)
	redirect, _ := out["redirect"].(string)
	if len(state) < 40 || redirect != "http://localhost:20128/callback" || out["paste"] != false {
		t.Fatalf("%v", out)
	}
	_, _, raw := o.call("GET", "/api/oauth/claude/authorize", "", o.cookie)
	for _, forbidden := range []string{"verifier", "code_verifier", "codeVerifier"} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("the answer names %s: %s", forbidden, raw)
		}
	}
	u, _ := url.Parse(out["authUrl"].(string))
	if u.Query().Get("redirect_uri") != redirect || u.Query().Get("state") != state {
		t.Fatalf("%s", u)
	}
}

func TestTheRouterPortOfTheRequestIsUsedOnlyWhenLocalAndAboutThisMachine(t *testing.T) {
	o := newOAuthRig(t, `{"access_token":"AT"}`)
	req := func(host, peer string) string {
		r := httptest.NewRequest("GET", "/api/oauth/claude/authorize", nil)
		r.RemoteAddr, r.Host = peer, host
		r.AddCookie(&nethttp.Cookie{Name: auth.CookieName, Value: o.cookie})
		rec := httptest.NewRecorder()
		o.route.ServeHTTP(rec, r)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		s, _ := out["redirect"].(string)
		return s
	}
	if got := req("localhost:9999", localPeer); got != "http://localhost:9999/callback" {
		t.Errorf("local: %s", got)
	}
	if got := req("evil.example:9999", localPeer); got != "http://localhost:20128/callback" {
		t.Errorf("a foreign Host built the redirect: %s", got)
	}
	r := httptest.NewRequest("GET", "/api/oauth/claude/authorize", nil)
	r.RemoteAddr, r.Host = "203.0.113.9:4000", "localhost:9999"
	r.Header.Set("X-Forwarded-Host", "evil.example")
	r.AddCookie(&nethttp.Cookie{Name: auth.CookieName, Value: o.cookie})
	rec := httptest.NewRecorder()
	o.route.ServeHTTP(rec, r)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code == 200 && (out["redirect"] != "http://localhost:20128/callback" || out["paste"] != true) {
		t.Errorf("a remote caller: %v", out)
	}
}

func TestThePastedAddressFinishesTheLoginAndTheOutcomeIsReadOnce(t *testing.T) {
	o := newOAuthRig(t, `{"access_token":"AT","refresh_token":"RT","expires_in":3600}`)
	state := o.start(t)["state"].(string)
	body, _ := json.Marshal(map[string]string{"state": state, "input": "http://localhost:20128/callback?code=THECODE&state=" + state})
	code, out, raw := o.call("POST", "/api/oauth/exchange", string(body), o.cookie)
	if code != 200 || out["success"] != true || out["connectionId"] == "" {
		t.Fatalf("%d %s", code, raw)
	}
	if strings.Contains(raw, "AT") && strings.Contains(raw, "accessToken") {
		t.Fatalf("the answer carries a token: %s", raw)
	}
	conns, err := o.st.Connections().List(context.Background(), repository.ConnFilter{Provider: "claude"})
	if err != nil || len(conns) != 1 || conns[0].Doc().Has("accessToken") == false {
		t.Fatalf("%d connections, %v", len(conns), err)
	}
	if _, st, _ := o.call("GET", "/api/oauth/status?state="+state, "", o.cookie); st["status"] != "done" || st["connectionId"] != out["connectionId"] {
		t.Fatalf("status: %v", st)
	}
	if _, st, _ := o.call("GET", "/api/oauth/status?state="+state, "", o.cookie); st["status"] != "unknown" {
		t.Fatalf("the outcome was read twice: %v", st)
	}
}

func TestAReplayAndAForgedStateFinishNothing(t *testing.T) {
	o := newOAuthRig(t, `{"access_token":"AT"}`)
	state := o.start(t)["state"].(string)
	for _, input := range []string{"code#forged", "http://h/cb?code=c&state=forged"} {
		b, _ := json.Marshal(map[string]string{"state": state, "input": input})
		if code, _, _ := o.call("POST", "/api/oauth/exchange", string(b), o.cookie); code != 400 {
			t.Errorf("%s: %d", input, code)
		}
	}
	if len(*o.seen) != 0 {
		t.Fatal("a forged state reached the token endpoint")
	}
	state = o.start(t)["state"].(string)
	b, _ := json.Marshal(map[string]string{"state": state, "input": "c"})
	if code, _, _ := o.call("POST", "/api/oauth/exchange", string(b), o.cookie); code != 200 {
		t.Fatalf("first: %d", code)
	}
	if code, _, _ := o.call("POST", "/api/oauth/exchange", string(b), o.cookie); code != 400 {
		t.Fatalf("replay: %d", code)
	}
	if len(*o.seen) != 1 {
		t.Fatalf("%d token calls", len(*o.seen))
	}
}

func TestTheCallbackPageForwardsAProviderError(t *testing.T) {
	o := newOAuthRig(t, `{"access_token":"AT"}`)
	state := o.start(t)["state"].(string)
	b, _ := json.Marshal(map[string]string{"state": state, "error": "access_denied"})
	code, out, _ := o.call("POST", "/api/oauth/callback", string(b), o.cookie)
	if code != 400 || out["code"] != oauth.CodeDenied {
		t.Fatalf("%d %v", code, out)
	}
	if len(*o.seen) != 0 {
		t.Fatal("a refused login reached the token endpoint")
	}
}

func TestBadBodiesAreRefusedBeforeAnyWork(t *testing.T) {
	o := newOAuthRig(t, `{"access_token":"AT"}`)
	for _, tc := range [][3]string{
		{"POST", "/api/oauth/exchange", `{"input":"c"}`},
		{"POST", "/api/oauth/exchange", `{"state":"s","input":""}`},
		{"POST", "/api/oauth/exchange", `not json`},
		{"POST", "/api/oauth/callback", `{"state":"s"}`},
		{"POST", "/api/oauth/callback", `{"code":"c"}`},
	} {
		if code, _, _ := o.call(tc[0], tc[1], tc[2], o.cookie); code != 400 {
			t.Errorf("%s: %d", tc[2], code)
		}
	}
	if len(*o.seen) != 0 {
		t.Fatal("a bad body reached the token endpoint")
	}
}

func TestAnUnknownProviderAndASwitchedOffLoginHaveClearAnswers(t *testing.T) {
	o := newOAuthRig(t, `{"access_token":"AT"}`)
	if code, _, _ := o.call("GET", "/api/oauth/openai/authorize", "", o.cookie); code != 404 {
		t.Errorf("no login: %d", code)
	}
	o.svc.ClientID = func(string) string { return "" }
	if code, _, raw := o.call("GET", "/api/oauth/claude/authorize", "", o.cookie); code != 400 || !strings.Contains(raw, "switched off") {
		t.Errorf("switched off: %d %s", code, raw)
	}
}

func TestStatusAndCompletionBelongToTheBrowserThatStartedTheLogin(t *testing.T) {
	o := newOAuthRig(t, `{"access_token":"AT"}`)
	state := o.start(t)["state"].(string)
	second, ok := o.auth.Issue(context.Background(), auth.MethodPassword, "other@x.test", "", false)
	if !ok || second.Value == o.cookie {
		t.Fatal("no second session")
	}
	if _, out, _ := o.call("GET", "/api/oauth/status?state="+state, "", second.Value); out["status"] != "unknown" {
		t.Errorf("another browser read the login: %v", out)
	}
	b, _ := json.Marshal(map[string]string{"state": state, "input": "c"})
	if code, _, _ := o.call("POST", "/api/oauth/exchange", string(b), second.Value); code != 400 {
		t.Errorf("another browser finished the login: %d", code)
	}
	if len(*o.seen) != 0 {
		t.Fatal("a foreign completion reached the token endpoint")
	}
	if _, out, _ := o.call("GET", "/api/oauth/status?state="+state, "", o.cookie); out["status"] != "pending" {
		t.Errorf("the owner: %v", out)
	}
}

func TestTheCallbackPageIsPublicStaticAndTouchesNoBrowserStorage(t *testing.T) {
	o := newOAuthRig(t, `{"access_token":"AT"}`)
	code, _, page := o.call("GET", "/callback", "", "")
	if code != 200 || !strings.Contains(page, `src="/static/js/callback.js"`) {
		t.Fatalf("%d %s", code, page)
	}
	if strings.Contains(page, "<script>") || strings.Contains(page, "onload") {
		t.Fatal("the page carries an inline script, which the CSP refuses")
	}
	code, _, script := o.call("GET", "/static/js/callback.js", "", "")
	if code != 200 {
		t.Fatalf("script: %d", code)
	}
	for _, banned := range []string{"postMessage", "BroadcastChannel", "localStorage", "sessionStorage", "indexedDB", "innerHTML", "document.cookie"} {
		if strings.Contains(script, banned) {
			t.Errorf("the callback script uses %s", banned)
		}
	}
	if strings.Index(script, "replaceState") > strings.Index(script, "fetch(") || !strings.Contains(script, "replaceState") {
		t.Fatal("the address must be replaced before anything is sent")
	}
	if code, _, _ := o.call("GET", "/static/js/other.js", "", ""); code == 200 {
		t.Fatal("an unlisted file is served")
	}
}
