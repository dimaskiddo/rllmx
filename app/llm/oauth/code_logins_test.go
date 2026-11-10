package oauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"rllmx/app/llm/upstream"
	"rllmx/app/store/repository"
)

// reroute is a client that sends every call to srv, whatever host the data
// names, so the real data files run against a test server.
func reroute(srv *httptest.Server) *http.Client {
	base := srv.Client()
	target, _ := url.Parse(srv.URL)
	return &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		r.URL.Scheme, r.URL.Host = target.Scheme, target.Host
		return base.Transport.RoundTrip(r)
	})}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// connectionWith is a stored login account of a provider with the given
// provider data.
func connectionWith(provider, psd string) repository.Connection {
	return repository.Connection{ID: "c1", Provider: provider, AuthType: "oauth", IsActive: true, Data: `{"providerSpecificData":` + psd + `}`}
}

type recorder struct {
	mu   sync.Mutex
	seen []seenCall
}

type seenCall struct {
	Path, Method, Type, Auth, Body string
}

func (r *recorder) serve(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		raw := make([]byte, 8192)
		n, _ := req.Body.Read(raw)
		r.mu.Lock()
		r.seen = append(r.seen, seenCall{req.URL.Path, req.Method, req.Header.Get("Content-Type"), req.Header.Get("Authorization"), string(raw[:n])})
		r.mu.Unlock()
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// codeService is a login service over the shipped registry. Its HTTP client
// sends every call to host, a test server, whatever address the data names, so
// the real data files are what runs.
func codeService(t *testing.T, reg *upstream.Registry, hc *http.Client, clients map[string]string) (*Service, *fakeAccounts) {
	t.Helper()
	accounts := &fakeAccounts{}
	return &Service{
		Registry: reg, ClientID: func(p string) string { return clients[p] },
		Sessions: NewSessions(), Tokens: &TokenClient{HTTP: hc, Now: func() time.Time { return time.Unix(1_700_000_000, 0) }},
		Now:      func() time.Time { return time.Unix(1_700_000_000, 0) },
		Accounts: func() Accounts { return accounts },
	}, accounts
}

func TestXAIAuthorizeAddressAndRedirect(t *testing.T) {
	svc, _ := codeService(t, registry(t), nil, map[string]string{"xai": "XAI-ID"})
	// no listener: the port may be busy, and paste mode is what the dashboard shows then
	s, err := svc.Start(StartRequest{Provider: "xai", Bind: "b", Local: true, RouterPort: 20128})
	if err != nil {
		t.Fatal(err)
	}
	if s.Redirect != "http://127.0.0.1:56121/callback" {
		t.Fatalf("redirect %s", s.Redirect)
	}
	u, _ := url.Parse(s.AuthURL)
	q := u.Query()
	if u.Host != "auth.x.ai" || q.Get("client_id") != "XAI-ID" || q.Get("code_challenge_method") != "S256" ||
		q.Get("plan") != "generic" || q.Get("referrer") != "cli-proxy-api" || q.Get("state") != s.State {
		t.Fatalf("%s", s.AuthURL)
	}
	nonce := q.Get("nonce")
	if len(nonce) != 32 || strings.Trim(nonce, "0123456789abcdef") != "" {
		t.Fatalf("nonce %q", nonce)
	}
	// the nonce parameter comes after the standard ones and before plan
	order := []string{"response_type", "client_id", "redirect_uri", "scope", "code_challenge", "code_challenge_method", "nonce", "plan", "referrer", "state"}
	last := -1
	for _, k := range order {
		i := strings.Index(s.AuthURL, "&"+k+"=")
		if k == "response_type" {
			i = strings.Index(s.AuthURL, "?"+k+"=")
		}
		if i < 0 || i < last {
			t.Fatalf("%s is out of order in %s", k, s.AuthURL)
		}
		last = i
	}
	again, _ := svc.Start(StartRequest{Provider: "xai", Bind: "b", Local: true, RouterPort: 20128})
	if v := mustQuery(t, again.AuthURL).Get("nonce"); v == nonce {
		t.Fatal("two logins share a nonce")
	}
}

func mustQuery(t *testing.T, raw string) url.Values {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query()
}

func TestXAINamesTheAccountFromItsClaimsAndNeverFromTheSubject(t *testing.T) {
	jwt := func(claims string) string {
		return "h." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".s"
	}
	cases := []struct {
		name string
		t    Tokens
		want string
	}{
		{"id token email", Tokens{ID: jwt(`{"email":"a@x.example","sub":"s1"}`)}, "a@x.example"},
		{"preferred username", Tokens{ID: jwt(`{"preferred_username":"u@x.example","sub":"s1"}`)}, "u@x.example"},
		{"access token", Tokens{Access: jwt(`{"email":"b@x.example"}`)}, "b@x.example"},
		{"subject only", Tokens{ID: jwt(`{"sub":"s1"}`)}, ""},
		{"a space in the address", Tokens{ID: jwt(`{"email":"a b@x.example"}`)}, ""},
	}
	for _, tc := range cases {
		acc, err := xaiAccount(context.Background(), nil, Flow{}, tc.t, nil)
		if err != nil || acc.Email != tc.want {
			t.Errorf("%s: %q %v, want %q", tc.name, acc.Email, err, tc.want)
		}
	}
}

func TestAFullXAILoginSendsNoSecretAndStoresTheAccount(t *testing.T) {
	var rec recorder
	srv := rec.serve(t, 200, `{"access_token":"AT","refresh_token":"RT","expires_in":3600,"id_token":"h.`+base64.RawURLEncoding.EncodeToString([]byte(`{"email":"x@x.example"}`))+`.s"}`)
	svc, accounts := codeService(t, registry(t), reroute(srv), map[string]string{"xai": "XAI-ID"})
	s, err := svc.Start(StartRequest{Provider: "xai", Bind: "b", Local: true, RouterPort: 20128})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Complete(context.Background(), s.State, Input{Code: "CODE", State: s.State}, "b"); err != nil {
		t.Fatal(err)
	}
	form, _ := url.ParseQuery(rec.seen[0].Body)
	if form.Get("client_id") != "XAI-ID" || form.Get("redirect_uri") != "http://127.0.0.1:56121/callback" || form.Get("code_verifier") == "" || form.Has("client_secret") {
		t.Fatalf("%v", form)
	}
	if got := accounts.made[0]; got.Email != "x@x.example" || got.Fields["accessToken"] != `"AT"` {
		t.Fatalf("%+v", got)
	}
}

func TestThePublicRedirectIsTheGatewayAddressOrRefused(t *testing.T) {
	svc, _ := codeService(t, registry(t), nil, nil)
	app := &App{BaseURL: "https://git.example", ClientID: "app-id"}
	s, err := svc.Start(StartRequest{Provider: "gitlab", Bind: "b", Local: false, RouterPort: 20128, PublicBase: "https://gw.example", App: app})
	if err != nil {
		t.Fatal(err)
	}
	if s.Redirect != "https://gw.example/callback" || s.Paste {
		t.Fatalf("%+v", s)
	}
	if _, err := svc.Start(StartRequest{Provider: "gitlab", Bind: "b", RouterPort: 20128, App: app}); err != ErrPublicBase {
		t.Fatalf("no public base: %v", err)
	}
}

func TestGitLabTakesItsApplicationFromTheRequestAndNoOneElseDoes(t *testing.T) {
	svc, _ := codeService(t, registry(t), nil, map[string]string{"claude": "C"})
	if _, err := svc.Start(StartRequest{Provider: "gitlab", Bind: "b", PublicBase: "https://gw.example"}); err != ErrApp {
		t.Fatalf("gitlab without an application: %v", err)
	}
	if _, err := svc.Start(StartRequest{Provider: "claude", Bind: "b", RouterPort: 1, Local: true, App: &App{BaseURL: "https://git.example", ClientID: "x"}}); err != ErrApp {
		t.Fatalf("claude with an application: %v", err)
	}
	for name, app := range map[string]*App{
		"http":          {BaseURL: "http://git.example", ClientID: "x"},
		"a path":        {BaseURL: "https://git.example/sub", ClientID: "x"},
		"userinfo":      {BaseURL: "https://u:p@git.example", ClientID: "x"},
		"a query":       {BaseURL: "https://git.example?a=b", ClientID: "x"},
		"a trailing /":  {BaseURL: "https://git.example/", ClientID: "x"},
		"no id":         {BaseURL: "https://git.example"},
		"a spaced id":   {BaseURL: "https://git.example", ClientID: "a b"},
		"a long secret": {BaseURL: "https://git.example", ClientID: "x", ClientSecret: strings.Repeat("s", 257)},
	} {
		if _, err := svc.Start(StartRequest{Provider: "gitlab", Bind: "b", PublicBase: "https://gw.example", App: app}); err != ErrApp {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestAGitLabLoginUsesItsOwnServerAndSealsTheSecret(t *testing.T) {
	var rec recorder
	srv := rec.serve(t, 200, `{"access_token":"AT","refresh_token":"RT","expires_in":7200,"username":"ann","email":"ann@x.example","name":"Ann"}`)
	reg := registry(t)
	svc, accounts := codeService(t, reg, srv.Client(), nil)
	app := &App{BaseURL: srv.URL, ClientID: "APP-ID", ClientSecret: "APP-SECRET"}
	s, err := svc.Start(StartRequest{Provider: "gitlab", Bind: "b", PublicBase: "https://gw.example", App: app})
	if err != nil {
		t.Fatal(err)
	}
	if u := mustQuery(t, s.AuthURL); !strings.HasPrefix(s.AuthURL, srv.URL+"/oauth/authorize?") || u.Get("client_id") != "APP-ID" || strings.Contains(s.AuthURL, "APP-SECRET") {
		t.Fatalf("%s", s.AuthURL)
	}
	if _, err := svc.Complete(context.Background(), s.State, Input{Code: "CODE", State: s.State}, "b"); err != nil {
		t.Fatal(err)
	}
	form, _ := url.ParseQuery(rec.seen[0].Body)
	if rec.seen[0].Path != "/oauth/token" || form.Get("client_secret") != "APP-SECRET" || form.Get("client_id") != "APP-ID" || form.Get("redirect_uri") != "https://gw.example/callback" {
		t.Fatalf("%+v %v", rec.seen[0], form)
	}
	if rec.seen[1].Path != "/api/v4/user" || rec.seen[1].Auth != "Bearer AT" {
		t.Fatalf("%+v", rec.seen[1])
	}
	got := accounts.made[0]
	if got.Email != "ann@x.example" || got.AuthType != "oauth" {
		t.Fatalf("%+v", got)
	}
	for _, want := range []string{`"baseUrl":"` + srv.URL + `"`, `"clientId":"APP-ID"`, `"clientSecret":"APP-SECRET"`, `"authKind":"oauth"`, `"username":"ann"`} {
		if !strings.Contains(got.PSD, want) {
			t.Errorf("provider data lacks %s: %s", want, got.PSD)
		}
	}
}

func TestAGitLabLoginWithoutASecretSendsNone(t *testing.T) {
	var rec recorder
	srv := rec.serve(t, 200, `{"access_token":"AT","refresh_token":"RT"}`)
	svc, accounts := codeService(t, registry(t), srv.Client(), nil)
	s, _ := svc.Start(StartRequest{Provider: "gitlab", Bind: "b", PublicBase: "https://gw.example", App: &App{BaseURL: srv.URL, ClientID: "APP-ID"}})
	if _, err := svc.Complete(context.Background(), s.State, Input{Code: "CODE"}, "b"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rec.seen[0].Body, "client_secret") || strings.Contains(accounts.made[0].PSD, "clientSecret") {
		t.Fatalf("%s | %s", rec.seen[0].Body, accounts.made[0].PSD)
	}
}

func gitlabConn(base, secret string) repository.Connection {
	psd := `{"baseUrl":"` + base + `","clientId":"APP-ID"`
	if secret != "" {
		psd += `,"clientSecret":"` + secret + `"`
	}
	return connectionWith("gitlab", psd+`}`)
}

func TestAGitLabAccountRenewsWithItsOwnApplication(t *testing.T) {
	var rec recorder
	srv := rec.serve(t, 200, `{"access_token":"NEW","refresh_token":"RT2","expires_in":7200}`)
	r := &Refresher{
		Flow: func(p string) (Flow, bool) {
			f, err := FlowFor(registry(t), func(string) string { return "" }, nil, p)
			return f, err == nil
		},
	}
	conn := gitlabConn(srv.URL, "APP-SECRET")
	f, ok := r.flowOf(conn)
	if !ok || f.ClientID != "APP-ID" || f.ClientSecret != "APP-SECRET" || f.TokenURL != srv.URL+"/oauth/token" {
		t.Fatalf("%+v %v", f, ok)
	}
	c := &TokenClient{HTTP: srv.Client()}
	if _, err := c.Refresh(context.Background(), f, "RT", nil); err != nil {
		t.Fatal(err)
	}
	form, _ := url.ParseQuery(rec.seen[0].Body)
	if form.Get("client_secret") != "APP-SECRET" || form.Get("client_id") != "APP-ID" || form.Get("refresh_token") != "RT" {
		t.Fatalf("%v", form)
	}
	if _, ok := r.flowOf(gitlabConn("http://evil.example", "")); ok {
		t.Fatal("an account with a bad server address was renewed")
	}
	if _, ok := r.flowOf(connectionWith("gitlab", `{}`)); ok {
		t.Fatal("an account with no application was renewed")
	}
}

func TestThePATRouteChecksTheTokenAgainstTheServerItNames(t *testing.T) {
	var rec recorder
	srv := rec.serve(t, 200, `{"username":"ann","email":"ann@x.example","name":"Ann"}`)
	svc, accounts := codeService(t, registry(t), srv.Client(), nil)
	id, err := svc.ConnectToken(context.Background(), "gitlab", srv.URL, "glpat-1234")
	if err != nil || id == "" {
		t.Fatalf("%q %v", id, err)
	}
	if rec.seen[0].Path != "/api/v4/user" || rec.seen[0].Auth != "Bearer glpat-1234" {
		t.Fatalf("%+v", rec.seen[0])
	}
	got := accounts.made[0]
	if got.Fields["accessToken"] != `"glpat-1234"` || got.Fields["refreshToken"] != "" || got.Fields["expiresAt"] != "" || got.Email != "ann@x.example" ||
		!strings.Contains(got.PSD, `"authKind":"personal_access_token"`) || !strings.Contains(got.PSD, `"baseUrl":"`+srv.URL+`"`) {
		t.Fatalf("%+v", got)
	}
}

func TestThePATRouteRefusesWhatItShouldNotCall(t *testing.T) {
	var rec recorder
	srv := rec.serve(t, 401, `{"message":"401 Unauthorized"}`)
	svc, accounts := codeService(t, registry(t), srv.Client(), map[string]string{"claude": "C"})
	for name, tc := range map[string]struct{ provider, base, token string }{
		"a rejected token":       {"gitlab", srv.URL, "glpat-1"},
		"an address with a path": {"gitlab", srv.URL + "/sub", "glpat-1"},
		"plain http":             {"gitlab", "http://git.example", "glpat-1"},
		"no token":               {"gitlab", srv.URL, ""},
		"a control character":    {"gitlab", srv.URL, "a\nb"},
		"a provider with no PAT": {"claude", srv.URL, "t"},
	} {
		if _, err := svc.ConnectToken(context.Background(), tc.provider, tc.base, tc.token); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if _, err := svc.ConnectToken(context.Background(), "claude", srv.URL, "t"); err != ErrUnsupported {
		t.Errorf("a provider with no PAT: %v, want the unsupported error", err)
	}
	if len(accounts.made) != 0 {
		t.Fatalf("%d accounts were stored", len(accounts.made))
	}
	if len(rec.seen) != 1 {
		t.Fatalf("%d calls reached the server, want only the rejected token's", len(rec.seen))
	}
}

func clineCode(doc string) string { return base64.RawStdEncoding.EncodeToString([]byte(doc)) }

func TestClineCodeCarriesTheTokens(t *testing.T) {
	doc := `{"accessToken":"AT","refreshToken":"RT","email":"c@x.example","expiresAt":"2023-11-14T22:13:20Z","firstName":"C"}`
	now := time.Unix(1_700_000_000, 0)
	std := base64.StdEncoding.EncodeToString([]byte(doc))
	cases := map[string]string{
		"unpadded standard": clineCode(doc),
		"padded standard":   std,
		"url-safe":          strings.NewReplacer("+", "-", "/", "_").Replace(clineCode(doc)),
		"trailing junk":     clineCode(doc + "\x00\x00 junk"),
	}
	for name, code := range cases {
		tok, ok := clineTokens(code, now)
		if !ok || tok.Access != "AT" || tok.Refresh != "RT" || tok.Email != "c@x.example" || tok.ExpiresIn != 1 {
			t.Errorf("%s: %+v %v", name, tok, ok)
		}
	}
	tok, _ := clineTokens(clineCode(strings.Replace(doc, "2023-11-14T22:13:20Z", "2023-11-14T22:13:30Z", 1)), now)
	if tok.ExpiresIn != 10 {
		t.Fatalf("expiresIn %d, want 10", tok.ExpiresIn)
	}
}

func TestAClineCodeThatHoldsNoTokensIsNotOne(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	for name, code := range map[string]string{
		"empty":           "",
		"not base64":      "!!!",
		"not json":        clineCode("plain text"),
		"no access token": clineCode(`{"refreshToken":"x"}`),
		"an array":        clineCode(`[1]`),
		"a control byte":  clineCode(`{"accessToken":"a\u0000b"}`),
		"too long":        clineCode(`{"accessToken":"` + strings.Repeat("a", maxClineCode) + `"}`),
	} {
		if _, ok := clineTokens(code, now); ok {
			t.Errorf("%s was read as tokens", name)
		}
	}
}

func clineFlow(t *testing.T, id, url string) Flow {
	t.Helper()
	e, ok := registry(t).ByID(id)
	if !ok {
		t.Fatal(id)
	}
	f, err := NewFlow(e, "")
	if err != nil {
		t.Fatal(err)
	}
	f.TokenURL, f.Refresh.URL = url+"/auth/token", url+"/auth/refresh"
	return f
}

func TestAClineCodeThatIsNotTokensIsTradedAtTheTokenAddress(t *testing.T) {
	var rec recorder
	srv := rec.serve(t, 200, `{"success":true,"data":{"accessToken":"AT","refreshToken":"RT","expiresAt":"2023-11-14T22:14:20Z","userInfo":{"email":"u@x.example"}}}`)
	c := &TokenClient{HTTP: srv.Client(), Now: func() time.Time { return time.Unix(1_700_000_000, 0) }}
	tok, err := c.Exchange(context.Background(), clineFlow(t, "cline", srv.URL), "OPAQUE", "https://gw.example/callback", "", "S")
	if err != nil || tok.Access != "AT" || tok.Refresh != "RT" || tok.Email != "u@x.example" || tok.ExpiresIn != 60 {
		t.Fatalf("%+v %v", tok, err)
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(rec.seen[0].Body), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"grant_type": "authorization_code", "code": "OPAQUE", "client_type": "extension", "redirect_uri": "https://gw.example/callback"}
	if rec.seen[0].Type != "application/json" || len(got) != len(want) {
		t.Fatalf("%+v %v", rec.seen[0], got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

func TestAClineLoginNeverCallsTheProviderWhenTheCodeHoldsTheTokens(t *testing.T) {
	var rec recorder
	srv := rec.serve(t, 500, `{}`)
	c := &TokenClient{HTTP: srv.Client()}
	code := clineCode(`{"accessToken":"AT","refreshToken":"RT","email":"c@x.example"}`)
	tok, err := c.Exchange(context.Background(), clineFlow(t, "clinepass", srv.URL), code, "https://gw.example/callback", "", "S")
	if err != nil || tok.Access != "AT" || len(rec.seen) != 0 {
		t.Fatalf("%+v %v %d calls", tok, err, len(rec.seen))
	}
}

func TestAClineRenewalUsesItsOwnBodyAddressAndEnvelope(t *testing.T) {
	var rec recorder
	srv := rec.serve(t, 200, `{"data":{"accessToken":"NEW","expiresAt":"2023-11-14T22:14:20Z"}}`)
	c := &TokenClient{HTTP: srv.Client(), Now: func() time.Time { return time.Unix(1_700_000_000, 0) }}
	tok, err := c.Refresh(context.Background(), clineFlow(t, "cline", srv.URL), "OLD-RT", nil)
	if err != nil || tok.Access != "NEW" || tok.Refresh != "OLD-RT" || tok.ExpiresIn != 60 {
		t.Fatalf("%+v %v", tok, err)
	}
	if rec.seen[0].Path != "/auth/refresh" {
		t.Fatalf("renewal went to %s", rec.seen[0].Path)
	}
	var got map[string]string
	_ = json.Unmarshal([]byte(rec.seen[0].Body), &got)
	if got["refreshToken"] != "OLD-RT" || got["grantType"] != "refresh_token" || got["clientType"] != "extension" || len(got) != 3 {
		t.Fatalf("%v", got)
	}
}

func TestAClineRenewalThatIsRefusedIsAClass(t *testing.T) {
	var rec recorder
	srv := rec.serve(t, 400, `{"error":"invalid_grant","note":"SECRET-TEXT"}`)
	c := &TokenClient{HTTP: srv.Client()}
	_, err := c.Refresh(context.Background(), clineFlow(t, "cline", srv.URL), "RT", nil)
	if ClassOf(err) != ClassInvalidGrant || strings.Contains(err.Error(), "SECRET-TEXT") {
		t.Fatalf("%v", err)
	}
}

func TestAClineLoginHasNoPKCEAndSendsNoClientID(t *testing.T) {
	svc, _ := codeService(t, registry(t), nil, nil)
	s, err := svc.Start(StartRequest{Provider: "cline", Bind: "b", PublicBase: "https://gw.example"})
	if err != nil {
		t.Fatal(err)
	}
	want := "https://api.cline.bot/api/v1/auth/authorize?client_type=extension&callback_url=https%3A%2F%2Fgw.example%2Fcallback&redirect_uri=https%3A%2F%2Fgw.example%2Fcallback&state=" + s.State
	if s.AuthURL != want {
		t.Fatalf("\n got %s\nwant %s", s.AuthURL, want)
	}
	for _, banned := range []string{"client_id", "code_challenge", "response_type", "scope"} {
		if strings.Contains(s.AuthURL, banned) {
			t.Errorf("the address holds %s", banned)
		}
	}
	e, _ := svc.Sessions.m[s.State]
	if e == nil || e.Verifier != "" {
		t.Fatal("a code login kept a verifier")
	}
}

func TestPastingAClineCallbackAddressFinishesTheLogin(t *testing.T) {
	svc, accounts := codeService(t, registry(t), nil, nil)
	s, _ := svc.Start(StartRequest{Provider: "cline", Bind: "b", PublicBase: "https://gw.example"})
	code := clineCode(`{"accessToken":"AT","refreshToken":"RT","email":"c@x.example","expiresAt":"2023-11-15T00:00:00Z"}`)
	in, err := ParseInput("https://gw.example/callback?code=" + url.QueryEscape(code) + "&state=" + s.State)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Complete(context.Background(), s.State, in, "b"); err != nil {
		t.Fatal(err)
	}
	got := accounts.made[0]
	if got.Provider != "cline" || got.Email != "c@x.example" || got.Fields["accessToken"] != `"AT"` || got.Fields["refreshToken"] != `"RT"` {
		t.Fatalf("%+v", got)
	}
}

func TestParseInputKeepsAPlusThatFormDecodingTurnedIntoASpace(t *testing.T) {
	in, err := ParseInput("https://gw.example/callback?code=ab+cd/ef&state=S")
	if err != nil || in.Code != "ab+cd/ef" {
		t.Fatalf("%+v %v", in, err)
	}
	for _, bad := range []string{"ab cd", "ab\ncd", "ab\tcd", "ab\x00cd", "https://gw.example/callback?code=ab%0Acd"} {
		if _, err := ParseInput(bad); err == nil {
			t.Fatalf("%q was accepted", bad)
		}
	}
	long := strings.Repeat("a", 12<<10)
	if in, err := ParseInput("https://gw.example/callback?code=" + long + "&state=S"); err != nil || len(in.Code) != len(long) {
		t.Fatalf("a code of two JWTs was refused: %v", err)
	}
	if _, err := ParseInput("https://gw.example/callback?code=" + strings.Repeat("a", 17<<10)); err == nil {
		t.Fatal("an oversized code was accepted")
	}
}

func FuzzClineTokens(f *testing.F) {
	for _, s := range []string{"", "e30", clineCode(`{"accessToken":"a"}`), clineCode(`{"accessToken":"a","expiresAt":"x"}`), "////", "a=b"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, code string) {
		tok, ok := clineTokens(code, time.Unix(1_700_000_000, 0))
		if ok && (tok.Access == "" || !credentialText(tok.Access) || tok.ExpiresIn < 0) {
			t.Fatalf("accepted %q as %+v", code, tok)
		}
	})
}

func TestEveryNonceIsUnique(t *testing.T) {
	seen := map[string]bool{}
	for range 200 {
		n, err := NewNonce()
		if err != nil || len(n) != 32 || seen[n] {
			t.Fatalf("%q %v", n, err)
		}
		seen[n] = true
	}
}
