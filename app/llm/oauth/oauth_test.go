package oauth_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"rllmx/app/llm/oauth"
	"rllmx/tests/login"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"rllmx/app/llm/upstream"
	"rllmx/app/store/repository"
)

func registry(t *testing.T) *upstream.Registry {
	t.Helper()
	reg, err := upstream.Default()
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func flowFor(t *testing.T, id string) oauth.Flow {
	t.Helper()
	e, ok := registry(t).ByID(id)
	if !ok {
		t.Fatalf("no provider %s", id)
	}
	f, err := oauth.NewFlow(e, "client-"+id)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestTheAuthorizeAddressIsExactPerProvider(t *testing.T) {
	claude := flowFor(t, "claude").URL("http://localhost:20128/callback", "ST", "CH", "NO")
	wantClaude := "https://claude.ai/oauth/authorize?response_type=code&client_id=client-claude&redirect_uri=http%3A%2F%2Flocalhost%3A20128%2Fcallback" +
		"&scope=org%3Acreate_api_key%20user%3Aprofile%20user%3Ainference&code_challenge=CH&code_challenge_method=S256&code=true&state=ST"
	if claude != wantClaude {
		t.Fatalf("claude:\n got %s\nwant %s", claude, wantClaude)
	}
	codex := flowFor(t, "codex").URL("http://localhost:1455/auth/callback", "ST", "CH", "NO")
	wantCodex := "https://auth.openai.com/oauth/authorize?response_type=code&client_id=client-codex&redirect_uri=http%3A%2F%2Flocalhost%3A1455%2Fauth%2Fcallback" +
		"&scope=openid%20profile%20email%20offline_access&code_challenge=CH&code_challenge_method=S256" +
		"&id_token_add_organizations=true&codex_cli_simplified_flow=true&originator=codex_cli_rs&state=ST"
	if codex != wantCodex {
		t.Fatalf("codex:\n got %s\nwant %s", codex, wantCodex)
	}
}

func TestTheServerDecidesTheRedirect(t *testing.T) {
	if got := flowFor(t, "claude").RedirectURL(9000, ""); got != "http://localhost:9000/callback" {
		t.Fatalf("claude: %s", got)
	}
	if got := flowFor(t, "codex").RedirectURL(9000, ""); got != "http://localhost:1455/auth/callback" {
		t.Fatalf("codex ignores the router port: %s", got)
	}
	if flowFor(t, "claude").ListenerPort() != 0 || flowFor(t, "codex").ListenerPort() != 1455 {
		t.Fatal("only codex needs a listener")
	}
}

func TestAProviderWithoutALoginOrAClientIDHasNone(t *testing.T) {
	e, _ := registry(t).ByID("openai")
	if _, err := oauth.NewFlow(e, "id"); err != oauth.ErrUnsupported {
		t.Fatalf("no block: %v", err)
	}
	e, _ = registry(t).ByID("claude")
	if _, err := oauth.NewFlow(e, ""); err != oauth.ErrDisabled {
		t.Fatalf("empty id: %v", err)
	}
}

func TestParseInput(t *testing.T) {
	cases := []struct {
		name, in string
		want     oauth.Input
		bad      bool
	}{
		{"address with query", "http://localhost:1455/auth/callback?code=abc&state=xyz", oauth.Input{Code: "abc", State: "xyz"}, false},
		{"address with fragment", "http://localhost:20128/callback#code=abc&state=xyz", oauth.Input{Code: "abc", State: "xyz"}, false},
		{"bare code", "abc", oauth.Input{Code: "abc"}, false},
		{"code and state", "abc#xyz", oauth.Input{Code: "abc", State: "xyz"}, false},
		{"padded", "  abc#xyz \n", oauth.Input{Code: "abc", State: "xyz"}, false},
		{"provider error", "http://localhost/callback?error=access_denied&state=xyz", oauth.Input{State: "xyz", Failed: true}, false},
		{"empty", "  ", oauth.Input{}, true},
		{"address without code", "http://localhost/callback?state=xyz", oauth.Input{}, true},
		{"space inside", "ab cd", oauth.Input{}, true},
		{"control byte", "ab\x00cd", oauth.Input{}, true},
		{"huge", strings.Repeat("a", oauth.MaxInput+1), oauth.Input{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := oauth.ParseInput(tc.in)
			if tc.bad {
				if err == nil {
					t.Fatalf("accepted: %+v", got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %+v, %v want %+v", got, err, tc.want)
			}
		})
	}
}

func FuzzParseInput(f *testing.F) {
	for _, s := range []string{"abc#xyz", "http://h/p?code=a&state=b", "https://h/p#code=a", "", "%", "http://[::1", "a#b#c"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		in, err := oauth.ParseInput(s)
		if err != nil {
			return
		}
		if !in.Failed && in.Code == "" {
			t.Fatalf("no code and no failure for %q", s)
		}
		if strings.ContainsAny(in.Code+in.State, " \t\r\n\x00") {
			t.Fatalf("unclean value for %q: %+v", s, in)
		}
	})
}

func TestSessionsAreSingleUseAndBound(t *testing.T) {
	now := time.Unix(1000, 0)
	s := oauth.NewSessions()
	s.Now = func() time.Time { return now }
	state, err := s.Put(oauth.Session{Provider: "claude", Verifier: "v", Bind: "mine"})
	if err != nil || len(state) < 40 {
		t.Fatalf("state %q: %v", state, err)
	}
	mine := func(x oauth.Session) bool { return oauth.SameBind(x.Bind, "mine") }
	if _, ok := s.Begin(state, func(x oauth.Session) bool { return oauth.SameBind(x.Bind, "theirs") }); ok {
		t.Fatal("a foreign browser began the login")
	}
	if _, ok := s.Begin("nope", mine); ok {
		t.Fatal("an unknown state began")
	}
	got, ok := s.Begin(state, mine)
	if !ok || got.Verifier != "v" {
		t.Fatalf("first begin: %+v %v", got, ok)
	}
	if _, ok := s.Begin(state, mine); ok {
		t.Fatal("a login began twice")
	}
	if st := s.Status(state, "mine"); st.State != "pending" {
		t.Fatalf("busy login: %+v", st)
	}
	s.End(state, "conn-1", "")
	if st := s.Status(state, "theirs"); st.State != "unknown" {
		t.Fatalf("a foreign browser read the outcome: %+v", st)
	}
	if st := s.Status(state, "mine"); st.State != "done" || st.ConnectionID != "conn-1" {
		t.Fatalf("outcome: %+v", st)
	}
	if st := s.Status(state, "mine"); st.State != "unknown" {
		t.Fatalf("the outcome was read twice: %+v", st)
	}
}

func TestAnEmptyBindingNeverMatches(t *testing.T) {
	if oauth.SameBind("", "") || oauth.SameBind("a", "") {
		t.Fatal("empty bindings matched")
	}
}

func TestSessionsExpire(t *testing.T) {
	now := time.Unix(1000, 0)
	s := oauth.NewSessions()
	s.Now = func() time.Time { return now }
	state, _ := s.Put(oauth.Session{Bind: "b"})
	now = now.Add(oauth.SessionLife + time.Second)
	if _, ok := s.Begin(state, func(oauth.Session) bool { return true }); ok {
		t.Fatal("an expired login began")
	}
	state, _ = s.Put(oauth.Session{Bind: "b"})
	s.Begin(state, func(oauth.Session) bool { return true })
	s.End(state, "c", "")
	now = now.Add(oauth.DoneLife + time.Second)
	if st := s.Status(state, "b"); st.State != "unknown" {
		t.Fatalf("an old outcome survived: %+v", st)
	}
}

func TestAClientCannotFloodTheStore(t *testing.T) {
	s := oauth.NewSessions()
	var first string
	for i := 0; i <= oauth.MaxPerBind; i++ {
		st, err := s.Put(oauth.Session{Bind: "same"})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = st
		}
	}
	if _, ok := s.Begin(first, func(oauth.Session) bool { return true }); ok {
		t.Fatal("the oldest login of a full binding survived")
	}
	if n := s.Waiting(0); n != oauth.MaxPerBind {
		t.Fatalf("%d logins wait", n)
	}
}

func tokenServer(t *testing.T, status int, body string, seen *[]string) (*httptest.Server, oauth.Flow) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := make([]byte, 4096)
		n, _ := r.Body.Read(raw)
		*seen = append(*seen, r.Header.Get("Content-Type")+"|"+string(raw[:n]))
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	f := flowFor(t, "claude")
	f.TokenURL = srv.URL
	return srv, f
}

func TestClaudeExchangesAsJSONWithTheState(t *testing.T) {
	var seen []string
	srv, f := tokenServer(t, 200, `{"access_token":"a","refresh_token":"r","expires_in":3600,"scope":"s"}`, &seen)
	c := &oauth.TokenClient{HTTP: srv.Client()}
	tok, err := c.Exchange(context.Background(), f, "CODE", "http://localhost:1/callback", "VER", "STATE")
	if err != nil || tok.Access != "a" || tok.Refresh != "r" || tok.ExpiresIn != 3600 {
		t.Fatalf("%+v %v", tok, err)
	}
	ct, body, _ := strings.Cut(seen[0], "|")
	if ct != "application/json" {
		t.Fatalf("content type %s", ct)
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"grant_type": "authorization_code", "client_id": "client-claude", "code": "CODE",
		"redirect_uri": "http://localhost:1/callback", "code_verifier": "VER", "state": "STATE",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

func TestCodexExchangesAsAFormWithoutTheState(t *testing.T) {
	var seen []string
	srv, f := tokenServer(t, 200, `{"access_token":"a"}`, &seen)
	f = flowFor(t, "codex")
	f.TokenURL = srv.URL
	c := &oauth.TokenClient{HTTP: srv.Client()}
	if _, err := c.Exchange(context.Background(), f, "CODE", "http://localhost:1455/auth/callback", "VER", "STATE"); err != nil {
		t.Fatal(err)
	}
	ct, body, _ := strings.Cut(seen[0], "|")
	if ct != "application/x-www-form-urlencoded" {
		t.Fatalf("content type %s", ct)
	}
	form, _ := url.ParseQuery(body)
	if form.Has("state") || form.Get("code") != "CODE" || form.Get("code_verifier") != "VER" || form.Get("client_id") != "client-codex" {
		t.Fatalf("form %v", form)
	}
}

func TestATokenFailureIsAClassAndNeverCarriesProviderText(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   oauth.Class
	}{
		{400, `{"error":"invalid_grant","error_description":"SECRET-TEXT"}`, oauth.ClassInvalidGrant},
		{400, `{"error":"refresh_token_reused"}`, oauth.ClassInvalidGrant},
		{500, `<html>SECRET-TEXT</html>`, oauth.ClassRejected},
		{200, `not json SECRET-TEXT`, oauth.ClassMalformed},
		{200, `{"refresh_token":"r"}`, oauth.ClassMalformed},
	}
	for _, tc := range cases {
		var seen []string
		srv, f := tokenServer(t, tc.status, tc.body, &seen)
		_, err := (&oauth.TokenClient{HTTP: srv.Client()}).Exchange(context.Background(), f, "c", "r", "v", "s")
		if oauth.ClassOf(err) != tc.want {
			t.Errorf("%d %s: class %q want %q", tc.status, tc.body, oauth.ClassOf(err), tc.want)
		}
		if err != nil && strings.Contains(err.Error(), "SECRET") {
			t.Errorf("the error carries provider text: %v", err)
		}
	}
}

func TestTheTokenClientFollowsNoRedirect(t *testing.T) {
	var hit bool
	other := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit = true }))
	defer other.Close()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	hc := srv.Client()
	hc.CheckRedirect = nil
	c := oauth.NewTokenClient(nil)
	c.HTTP.Transport = srv.Client().Transport
	f := flowFor(t, "claude")
	f.TokenURL = srv.URL
	if _, err := c.Exchange(context.Background(), f, "c", "r", "v", "s"); err == nil || hit {
		t.Fatalf("redirect followed: hit=%v err=%v", hit, err)
	}
}

func TestTheTokenClientRefusesAPrivateAddress(t *testing.T) {
	f := flowFor(t, "claude")
	f.TokenURL = "https://127.0.0.1:1/token"
	_, err := oauth.NewTokenClient(nil).Exchange(context.Background(), f, "c", "r", "v", "s")
	if oauth.ClassOf(err) != oauth.ClassTransport {
		t.Fatalf("%v", err)
	}
}

func jwt(payload string) string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none"}`)) + "." + enc([]byte(payload)) + "." + enc([]byte("sig"))
}

func TestCodexReadsTheAccountFromTheIDToken(t *testing.T) {
	acc := oauth.CodexAccount(oauth.Tokens{ID: jwt(`{"email":"a@x.test","https://api.openai.com/auth":{"chatgpt_account_id":"acct-1","chatgpt_plan_type":"plus"}}`)})
	if acc.Email != "a@x.test" {
		t.Fatalf("email %q", acc.Email)
	}
	if acc.PSD != `{"chatgptAccountId":"acct-1","chatgptPlanType":"plus"}` {
		t.Fatalf("psd %s", acc.PSD)
	}
	fallback := oauth.CodexAccount(oauth.Tokens{ID: "garbage", Access: jwt(`{"preferred_username":"b@x.test"}`)})
	if fallback.Email != "b@x.test" || fallback.PSD != "" {
		t.Fatalf("fallback %+v", fallback)
	}
	if got := oauth.CodexAccount(oauth.Tokens{ID: jwt(`{"account_id":"old","plan_type":"pro"}`)}); got.PSD != `{"chatgptAccountId":"old","chatgptPlanType":"pro"}` {
		t.Fatalf("legacy claims %s", got.PSD)
	}
}

type fakeAccounts struct {
	mu   sync.Mutex
	made []repository.ConnInput
}

func (f *fakeAccounts) Create(_ context.Context, in repository.ConnInput, _ *bool) (repository.Connection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.made = append(f.made, in)
	return repository.Connection{ID: "conn-" + string(rune('0'+len(f.made)))}, nil
}

func newService(t *testing.T, tokenURL string, hc *http.Client) (*oauth.Service, *fakeAccounts) {
	t.Helper()
	accounts := &fakeAccounts{}
	sessions := oauth.NewSessions()
	svc := &oauth.Service{
		Registry: registry(t), ClientID: func(p string) string { return "client-" + p },
		Sessions: sessions, Tokens: &oauth.TokenClient{HTTP: hc}, Now: func() time.Time { return time.Unix(1_700_000_000, 0) },
		Accounts: func() oauth.Accounts { return accounts },
		Dialects: oauth.Dialects{Hooks: map[string]oauth.Hook{"codex": func(_ context.Context, _ *oauth.TokenClient, _ oauth.Flow, t oauth.Tokens, _ map[string]string) (oauth.Account, error) {
			return oauth.CodexAccount(t), nil
		}}},
	}
	svc.Registry = login.FakeRegistry(t, tokenURL)
	return svc, accounts
}

// fakeRegistry holds the two seed logins with their token address replaced.
func fakeRegistry(t *testing.T, tokenURL string) *upstream.Registry {
	return fakeRegistryPort(t, tokenURL, 1455)
}

func fakeRegistryPort(t *testing.T, tokenURL string, port int) *upstream.Registry {
	t.Helper()
	entry := func(id, format, extra string) *fstest.MapFile {
		return &fstest.MapFile{Data: []byte(`{"id":"` + id + `","category":"oauth","transport":{"baseUrl":"https://x.example/v1","format":"` + format + `"},"models":[],` +
			`"oauth":{"flow":"code-pkce","authorizeUrl":"https://a.example/authorize","tokenUrl":"` + tokenURL + `","scope":"s",` + extra + `}}`)}
	}
	reg, err := upstream.Load(fstest.MapFS{
		"data/claude.json": entry("claude", "claude", `"redirect":"local","exchange":"json","echoState":true,"refresh":{"encoding":"json","leadSeconds":14400}`),
		"data/codex.json":  entry("codex", "openai-responses", `"redirect":"fixed","fixedPort":`+strconv.Itoa(port)+`,"callbackPath":"/auth/callback","exchange":"form","hook":"codex","refresh":{"encoding":"json","leadSeconds":600,"maxAgeSeconds":691200}`),
		"data/openai.json": {Data: []byte(`{"id":"openai","category":"apikey","transport":{"baseUrl":"https://x.example/v1"},"models":[]}`)},
	}, "data")
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func TestAFullLoginStoresTheAccountOnce(t *testing.T) {
	var seen []string
	srv, _ := tokenServer(t, 200, `{"access_token":"AT","refresh_token":"RT","expires_in":3600,"scope":"s"}`, &seen)
	svc, accounts := newService(t, srv.URL, srv.Client())

	started, err := startLogin(svc, "claude", true)
	if err != nil {
		t.Fatal(err)
	}
	if started.Paste || started.Redirect != "http://localhost:20128/callback" || started.State == "" {
		t.Fatalf("%+v", started)
	}
	u, _ := url.Parse(started.AuthURL)
	q := u.Query()
	if q.Get("state") != started.State || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		t.Fatalf("authorize query %v", q)
	}
	if strings.Contains(started.AuthURL, "code_verifier") {
		t.Fatal("the verifier is in the authorize address")
	}

	id, err := svc.Complete(context.Background(), started.State, oauth.Input{Code: "CODE", State: started.State}, "bind")
	if err != nil || id == "" {
		t.Fatalf("%q %v", id, err)
	}
	if len(accounts.made) != 1 {
		t.Fatalf("%d accounts", len(accounts.made))
	}
	in := accounts.made[0]
	if in.Provider != "claude" || in.AuthType != "oauth" || in.Fields["accessToken"] != `"AT"` || in.Fields["refreshToken"] != `"RT"` || in.Fields["testStatus"] != `"active"` {
		t.Fatalf("%+v", in)
	}
	if in.Fields["expiresAt"] != `"2023-11-14T23:13:20.000Z"` {
		t.Fatalf("expiresAt %s", in.Fields["expiresAt"])
	}
	if _, err := svc.Complete(context.Background(), started.State, oauth.Input{Code: "CODE"}, "bind"); err != oauth.ErrSession {
		t.Fatalf("a replay finished: %v", err)
	}
	if len(accounts.made) != 1 || len(seen) != 1 {
		t.Fatalf("a replay reached the provider or the store: %d %d", len(accounts.made), len(seen))
	}
}

func TestEveryCompletionPathChecksTheState(t *testing.T) {
	var seen []string
	srv, _ := tokenServer(t, 200, `{"access_token":"AT"}`, &seen)
	svc, accounts := newService(t, srv.URL, srv.Client())

	for name, run := range map[string]func(state string) error{
		"another state in the input": func(s string) error {
			_, err := svc.Complete(context.Background(), s, oauth.Input{Code: "C", State: "forged"}, "bind")
			return err
		},
		"another browser": func(s string) error {
			_, err := svc.Complete(context.Background(), s, oauth.Input{Code: "C"}, "other")
			return err
		},
		"an unknown state": func(string) error {
			_, err := svc.Complete(context.Background(), "unknown", oauth.Input{Code: "C"}, "bind")
			return err
		},
	} {
		started, err := startLogin(svc, "claude", true)
		if err != nil {
			t.Fatal(err)
		}
		if err := run(started.State); err == nil {
			t.Errorf("%s: finished", name)
		}
	}
	if len(seen) != 0 || len(accounts.made) != 0 {
		t.Fatalf("a bad completion reached the provider (%d) or the store (%d)", len(seen), len(accounts.made))
	}
}

func TestAProviderErrorEndsTheLoginWithAFixedCode(t *testing.T) {
	svc, accounts := newService(t, "https://unused.invalid/token", http.DefaultClient)
	started, _ := startLogin(svc, "claude", true)
	_, err := svc.Complete(context.Background(), started.State, oauth.Input{Failed: true}, "bind")
	var f *oauth.Failure
	if !asFailure(err, &f) || f.Code != oauth.CodeDenied {
		t.Fatalf("%v", err)
	}
	if st := svc.Sessions.Status(started.State, "bind"); st.State != "error" || st.Error != oauth.CodeDenied {
		t.Fatalf("%+v", st)
	}
	if len(accounts.made) != 0 {
		t.Fatal("an account was stored")
	}
}

func TestACodexLoginKeepsTheAccountIdentity(t *testing.T) {
	var seen []string
	body := `{"access_token":"AT","refresh_token":"RT","id_token":"` + jwt(`{"email":"a@x.test","https://api.openai.com/auth":{"chatgpt_account_id":"acct-1"}}`) + `"}`
	srv, _ := tokenServer(t, 200, body, &seen)
	svc, accounts := newService(t, srv.URL, srv.Client())
	started, err := startLogin(svc, "codex", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Complete(context.Background(), started.State, oauth.Input{Code: "C"}, "bind"); err != nil {
		t.Fatal(err)
	}
	in := accounts.made[0]
	if in.Email != "a@x.test" || in.PSD != `{"chatgptAccountId":"acct-1"}` {
		t.Fatalf("%+v", in)
	}
}

func TestADisabledLoginSendsNothing(t *testing.T) {
	svc, _ := newService(t, "https://unused.invalid/token", http.DefaultClient)
	svc.ClientID = func(string) string { return "" }
	if _, err := startLogin(svc, "claude", true); err != oauth.ErrDisabled {
		t.Fatalf("%v", err)
	}
	if _, err := startLogin(svc, "openai", true); err != oauth.ErrUnsupported {
		t.Fatalf("%v", err)
	}
	if _, err := startLogin(svc, "nothing", true); err != oauth.ErrUnsupported {
		t.Fatalf("%v", err)
	}
}

func asFailure(err error, out **oauth.Failure) bool {
	f, ok := err.(*oauth.Failure)
	*out = f
	return ok
}

// startLogin opens a login on the test port, for a browser that is or is not on
// this machine.
func startLogin(svc *oauth.Service, provider string, local bool) (oauth.Started, error) {
	return svc.Start(oauth.StartRequest{Provider: provider, Bind: "bind", Local: local, RouterPort: 20128})
}
