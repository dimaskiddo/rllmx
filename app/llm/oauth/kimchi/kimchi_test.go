package kimchi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"rllmx/app/llm/oauth"
	"rllmx/tests/login"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/upstream"
)

// kimchiRegistry is a provider file shaped like the real one: a login whose
// callback carries the token, validated against an address of its own.
func kimchiRegistry(t *testing.T, srv *httptest.Server) *upstream.Registry {
	t.Helper()
	// A data file holds only https addresses, which the fake's own address is.
	file := `{"id":"kc","category":"freeTier","executor":"kimchi",
		"transport":{"baseUrl":"` + srv.URL + `/openai/v1/chat/completions","format":"openai"},` +
		`"oauth":{"flow":"code","authorizeUrl":"` + srv.URL + `/cli-auth","tokenUrl":"` + srv.URL + `/supported",` +
		`"scope":"","redirect":"local","dialect":"kimchi","hook":"kimchi","noClient":true,` +
		`"params":[{"name":"callback","value":"{redirect}"}],` +
		`"endpoints":{"validate":"` + srv.URL + `/supported","user":"` + srv.URL + `/me"},"refresh":{"encoding":"json"}},` +
		`"models":[{"id":"m","name":"M"}]}`
	reg, err := upstream.Load(fstest.MapFS{"data/kc.json": {Data: []byte(file)}}, "data")
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

// kimchiServer answers the validate and profile calls.
func kimchiServer(t *testing.T, validate int, profile string) *httptest.Server {
	t.Helper()
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/supported":
			w.WriteHeader(validate)
		case "/me":
			_, _ = w.Write([]byte(profile))
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func TestKimchiDialectAcceptsAValidatedToken(t *testing.T) {
	srv := kimchiServer(t, http.StatusOK, `{"id":"42","username":"octo","email":"o@x.test"}`)
	accounts := &login.Accounts{}
	svc := &oauth.Service{
		Registry: kimchiRegistry(t, srv), ClientID: func(string) string { return "" },
		Sessions: oauth.NewSessions(), Tokens: &oauth.TokenClient{HTTP: srv.Client(), Dialects: kimchiTable()},
		Now: func() time.Time { return time.Unix(1_700_000_000, 0) }, Accounts: func() oauth.Accounts { return accounts },
	}
	toks, acc, err := completeKimchi(t, svc, "TOKEN-"+strings.Repeat("a", 60))
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if toks.Access == "" || acc.Email != "o@x.test" {
		t.Fatalf("tokens = %+v, account = %+v", toks, acc)
	}
	psd := gjson.Parse(acc.PSD)
	if psd.Get("username").String() != "octo" || psd.Get("userId").String() != "42" || psd.Get("authMethod").String() != "browser_token" {
		t.Fatalf("account data = %s", acc.PSD)
	}
}

func TestKimchiDialectRefusesATokenTheServiceRejects(t *testing.T) {
	srv := kimchiServer(t, http.StatusUnauthorized, `{}`)
	svc := &oauth.Service{
		Registry: kimchiRegistry(t, srv), ClientID: func(string) string { return "" },
		Sessions: oauth.NewSessions(), Tokens: &oauth.TokenClient{HTTP: srv.Client(), Dialects: kimchiTable()},
		Now: func() time.Time { return time.Unix(1_700_000_000, 0) }, Accounts: func() oauth.Accounts { return &login.Accounts{} },
	}
	_, _, err := completeKimchi(t, svc, "TOKEN")
	if oauth.ClassOf(err) != oauth.ClassRejected {
		t.Fatalf("err = %v, want a rejected class", err)
	}
}

func kimchiTable() oauth.Dialects {
	return oauth.Dialects{Code: map[string]oauth.CodeDialect{"kimchi": {Exchange: Exchange, Refresh: NoRefresh}}, Hooks: map[string]oauth.Hook{"kimchi": Account}}
}

// completeKimchi runs the login's exchange and hook, the way the engine does.
func completeKimchi(t *testing.T, svc *oauth.Service, token string) (oauth.Tokens, oauth.Account, error) {
	t.Helper()
	f, err := oauth.FlowFor(svc.Registry, func(string) string { return "" }, nil, "kc")
	if err != nil {
		t.Fatalf("flow: %v", err)
	}
	toks, err := svc.Tokens.Exchange(context.Background(), f, token, "", "", "state")
	if err != nil {
		return oauth.Tokens{}, oauth.Account{}, err
	}
	acc, err := Account(context.Background(), svc.Tokens, f, toks, nil)
	return toks, acc, err
}

func TestCallbackCarriesTheTokenParameter(t *testing.T) {
	// A login whose callback holds `token` rather than `code`.
	in, err := oauth.ParseInput("https://gw.example/callback?token=abc&state=s1")
	if err != nil || in.Code != "abc" || in.State != "s1" {
		t.Fatalf("input = %+v, %v", in, err)
	}
	// A code still wins when both are present.
	in, err = oauth.ParseInput("https://gw.example/callback?code=c&token=t&state=s")
	if err != nil || in.Code != "c" {
		t.Fatalf("input = %+v, %v", in, err)
	}
}
