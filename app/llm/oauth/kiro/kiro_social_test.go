package kiro

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"rllmx/app/llm/oauth"
	"strings"
	"sync"
	"testing"
	"time"

	"rllmx/app/llm/upstream"
	"rllmx/app/store/repository"
	"rllmx/tests/login"
)

var kiroTable = oauth.Dialects{
	Code: map[string]oauth.CodeDialect{"kiro-social": {
		Exchange: SocialExchange, Refresh: SocialRefresh, CustomURL: SocialAuthorize,
		Variant: Variant, Paste: true, RenewParams: map[string]string{"idp": "authMethod"},
	}},
	Hooks: map[string]oauth.Hook{"kiro": Account, "kiroSocial": SocialAccount},
}

// codeService runs the engine over the shipped data with this package's steps.
func codeService(t *testing.T, reg *upstream.Registry, hc *http.Client, clients map[string]string) (*oauth.Service, *login.Accounts) {
	t.Helper()
	accounts := &login.Accounts{}
	return &oauth.Service{
		Registry: reg, ClientID: func(p string) string { return clients[p] },
		Sessions: oauth.NewSessions(), Tokens: &oauth.TokenClient{HTTP: hc, Dialects: kiroTable},
		Dialects: kiroTable,
		Now:      func() time.Time { return time.Unix(1_700_000_000, 0) },
		Accounts: func() oauth.Accounts { return accounts },
	}, accounts
}

func registry(t *testing.T) *upstream.Registry { return login.Registry(t) }

// The dashboard offers Variants and Variant accepts them, from one list: a name
// can never be shown that the engine then refuses, or the other way round.
func TestEveryOfferedVariantIsAcceptedAndNothingElseIs(t *testing.T) {
	flow := oauth.Flow{OAuth: upstream.OAuth{Dialect: kiroSocialDialect}}
	for _, v := range Variants() {
		if _, err := Variant(flow, v); err != nil {
			t.Errorf("%q is offered but refused: %v", v, err)
		}
	}
	for _, v := range []string{"", "Google", "gitlab", "google ", "apple"} {
		if _, err := Variant(flow, v); !errors.Is(err, oauth.ErrVariant) {
			t.Errorf("%q is accepted although the dashboard does not offer it: %v", v, err)
		}
	}
}

func TestTheKiroSocialAuthorizeAddressIsTheProvidersOwn(t *testing.T) {
	svc, _ := codeService(t, registry(t), nil, map[string]string{})
	svc.Sessions = oauth.NewSessions()
	for _, idp := range []string{upstream.VariantGoogle, upstream.VariantGitHub} {
		s, err := svc.Start(oauth.StartRequest{Provider: "kiro-social", Bind: "b", Local: true, RouterPort: 20128, Variant: idp})
		if err != nil {
			t.Fatalf("%s: %v", idp, err)
		}
		q := mustQuery(t, s.AuthURL)
		if q.Get("idp") != idp {
			t.Errorf("%s: idp %q", idp, q.Get("idp"))
		}
		if q.Get("redirect_uri") != kiroSocialRedirect {
			t.Errorf("%s: redirect %q, want the provider's own scheme", idp, q.Get("redirect_uri"))
		}
		// The provider registers this address, so a change to it would be
		// refused there rather than here: pin it.
		if kiroSocialRedirect != "kiro://kiro.kiroAgent/authenticate-success" {
			t.Fatalf("the redirect is %q, which AWS Cognito does not answer", kiroSocialRedirect)
		}
		if q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256" {
			t.Errorf("%s: no PKCE in the address: %s", idp, s.AuthURL)
		}
		if q.Get("state") != s.State {
			t.Errorf("%s: state %q", idp, q.Get("state"))
		}
		if !s.Paste {
			t.Errorf("%s: the login must be finished by pasting, since nothing receives the scheme", idp)
		}
		if u, _ := url.Parse(s.AuthURL); u.Host != "prod.us-east-1.auth.desktop.kiro.dev" {
			t.Errorf("%s: host %q", idp, u.Host)
		}
	}
}

func TestAKiroSocialLoginMustNameAnIdentityProvider(t *testing.T) {
	svc, _ := codeService(t, registry(t), nil, map[string]string{})
	for name, variant := range map[string]string{"none": "", "unknown": "facebook"} {
		if _, err := svc.Start(oauth.StartRequest{Provider: "kiro-social", Bind: "b", Local: true, RouterPort: 20128, Variant: variant}); err != oauth.ErrVariant {
			t.Errorf("%s: %v, want ErrVariant", name, err)
		}
	}
}

func TestAKiroSocialPasteIsExchangedWithTheProviderRedirect(t *testing.T) {
	rec := &recorder{}
	srv := rec.serve(t, 200, `{"accessToken":"AT","refreshToken":"RT","expiresIn":3600,"profileArn":"arn:aws:codewhisperer:us-east-1:1:profile/X"}`)
	accounts := &login.Accounts{}
	svc := &oauth.Service{
		Registry: registry(t), ClientID: func(string) string { return "" }, Sessions: oauth.NewSessions(),
		Tokens:   &oauth.TokenClient{HTTP: login.Reroute(srv), Dialects: kiroTable, Now: func() time.Time { return time.Unix(1_700_000_000, 0) }},
		Dialects: kiroTable,
		Now:      func() time.Time { return time.Unix(1_700_000_000, 0) },
		Accounts: func() oauth.Accounts { return accounts },
	}

	s, err := svc.Start(oauth.StartRequest{Provider: "kiro-social", Bind: "b", Local: true, RouterPort: 20128, Variant: upstream.VariantGoogle})
	if err != nil {
		t.Fatal(err)
	}
	in, err := oauth.ParseInput("kiro://kiro.kiroAgent/authenticate-success?code=THE-CODE&state=" + s.State)
	if err != nil {
		t.Fatalf("the pasted address was refused: %v", err)
	}
	id, err := svc.Complete(context.Background(), s.State, in, "b")
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if id == "" {
		t.Fatal("no account")
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.seen) == 0 {
		t.Fatal("no call")
	}
	body := rec.seen[0].Body
	for _, want := range []string{`"code":"THE-CODE"`, `"redirect_uri":"` + kiroSocialRedirect + `"`, `"code_verifier":"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the exchange body lacks %s: %s", want, body)
		}
	}
	if !strings.Contains(accounts.All()[0].PSD, `"authMethod":"google"`) {
		t.Errorf("the identity provider was not recorded: %s", accounts.All()[0].PSD)
	}
	if !strings.Contains(accounts.All()[0].PSD, `"profileArn":"arn:aws:codewhisperer:`) {
		t.Errorf("the profile was not recorded: %s", accounts.All()[0].PSD)
	}
}

func TestACustomSchemePasteIsRead(t *testing.T) {
	in, err := oauth.ParseInput("kiro://kiro.kiroAgent/authenticate-success?code=ABC&state=ST")
	if err != nil {
		t.Fatalf("%v", err)
	}
	if in.Code != "ABC" || in.State != "ST" {
		t.Errorf("code %q state %q", in.Code, in.State)
	}
}

func TestASocialRenewalUsesTheAccountsIdentityProvider(t *testing.T) {
	rec := &recorder{}
	srv := rec.serve(t, 200, `{"accessToken":"AT2","expiresIn":3600}`)
	svc := &oauth.Service{
		Registry: registry(t), ClientID: func(string) string { return "" },
		Tokens: &oauth.TokenClient{HTTP: login.Reroute(srv), Dialects: kiroTable, Now: func() time.Time { return time.Unix(1_700_000_000, 0) }},
		Now:    func() time.Time { return time.Unix(1_700_000_000, 0) },
	}
	conn := connectionWith("kiro-social", `{"authMethod":"github","profileArn":"arn:aws:codewhisperer:us-east-1:1:profile/X"}`)
	r := &oauth.Refresher{Dialects: kiroTable, Flow: func(p string) (oauth.Flow, bool) { f, err := renamedFlow(svc, p); return f, err == nil }}
	f, ok := r.FlowOf(conn)
	if !ok || f.Dialect != kiroSocialDialect {
		t.Fatalf("flow %+v ok %v", f, ok)
	}
	if oauth.Param(f, kiroIdpParam) != "github" {
		t.Errorf("the account's identity provider was lost: %+v", f.Params)
	}
	tok, err := svc.Tokens.Refresh(context.Background(), f, "RT", nil)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if tok.Access != "AT2" || tok.Refresh != "RT" {
		t.Errorf("tokens %+v", tok)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.seen) != 1 || rec.seen[0].Path != "/oauth/token" || !strings.Contains(rec.seen[0].Body, `"refreshToken":"RT"`) {
		t.Errorf("calls %+v", rec.seen)
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

// recorder is a TLS server that keeps the bodies it receives.
type seenCall struct{ Path, Body string }

type recorder struct {
	mu   sync.Mutex
	seen []seenCall
}

func (r *recorder) serve(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		raw := make([]byte, 4096)
		n, _ := req.Body.Read(raw)
		r.mu.Lock()
		r.seen = append(r.seen, seenCall{Path: req.URL.Path, Body: string(raw[:n])})
		r.mu.Unlock()
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func connectionWith(provider, psd string) repository.Connection {
	return repository.Connection{ID: "c1", Provider: provider, AuthType: "oauth", IsActive: true, Data: `{"providerSpecificData":` + psd + `}`}
}

func renamedFlow(svc *oauth.Service, id string) (oauth.Flow, error) {
	return oauth.FlowFor(svc.Registry, func(string) string { return "" }, nil, id)
}
