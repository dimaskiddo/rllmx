package oauth

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"rllmx/app/upstream"
)

func TestTheKiroSocialAuthorizeAddressIsTheProvidersOwn(t *testing.T) {
	svc, _ := codeService(t, registry(t), nil, map[string]string{})
	svc.Sessions = NewSessions()
	for _, idp := range []string{upstream.VariantGoogle, upstream.VariantGitHub} {
		s, err := svc.Start(StartRequest{Provider: "kiro-social", Bind: "b", Local: true, RouterPort: 20128, Variant: idp})
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
		if _, err := svc.Start(StartRequest{Provider: "kiro-social", Bind: "b", Local: true, RouterPort: 20128, Variant: variant}); err != ErrVariant {
			t.Errorf("%s: %v, want ErrVariant", name, err)
		}
	}
}

func TestAKiroSocialPasteIsExchangedWithTheProviderRedirect(t *testing.T) {
	rec := &recorder{}
	srv := rec.serve(t, 200, `{"accessToken":"AT","refreshToken":"RT","expiresIn":3600,"profileArn":"arn:aws:codewhisperer:us-east-1:1:profile/X"}`)
	accounts := &fakeAccounts{}
	svc := &Service{
		Registry: registry(t), ClientID: func(string) string { return "" }, Sessions: NewSessions(),
		Tokens:   &TokenClient{HTTP: reroute(srv), Now: func() time.Time { return time.Unix(1_700_000_000, 0) }},
		Now:      func() time.Time { return time.Unix(1_700_000_000, 0) },
		Accounts: func() Accounts { return accounts },
	}

	s, err := svc.Start(StartRequest{Provider: "kiro-social", Bind: "b", Local: true, RouterPort: 20128, Variant: upstream.VariantGoogle})
	if err != nil {
		t.Fatal(err)
	}
	in, err := ParseInput("kiro://kiro.kiroAgent/authenticate-success?code=THE-CODE&state=" + s.State)
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
	if !strings.Contains(accounts.made[0].PSD, `"authMethod":"google"`) {
		t.Errorf("the identity provider was not recorded: %s", accounts.made[0].PSD)
	}
	if !strings.Contains(accounts.made[0].PSD, `"profileArn":"arn:aws:codewhisperer:`) {
		t.Errorf("the profile was not recorded: %s", accounts.made[0].PSD)
	}
}

func TestACustomSchemePasteIsRead(t *testing.T) {
	in, err := ParseInput("kiro://kiro.kiroAgent/authenticate-success?code=ABC&state=ST")
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
	svc := &Service{
		Registry: registry(t), ClientID: func(string) string { return "" },
		Tokens: &TokenClient{HTTP: reroute(srv), Now: func() time.Time { return time.Unix(1_700_000_000, 0) }},
		Now:    func() time.Time { return time.Unix(1_700_000_000, 0) },
	}
	conn := connectionWith("kiro-social", `{"authMethod":"github","profileArn":"arn:aws:codewhisperer:us-east-1:1:profile/X"}`)
	r := &Refresher{Flow: func(p string) (Flow, bool) { f, err := svc.flow(p); return f, err == nil }}
	f, ok := r.flowOf(conn)
	if !ok || f.Dialect != kiroSocialDialect {
		t.Fatalf("flow %+v ok %v", f, ok)
	}
	if paramOf(f, kiroIdpParam) != "github" {
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
