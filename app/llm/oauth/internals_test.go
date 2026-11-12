package oauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"rllmx/app/llm/upstream"
	"rllmx/app/store/repository"
)

// connectionWith is an account read back from the store's own shape.
func connectionWith(provider, psd string) repository.Connection {
	return repository.Connection{ID: "c1", Provider: provider, AuthType: "oauth", IsActive: true, Data: `{"providerSpecificData":` + psd + `}`}
}

func gitlabConn(base, secret string) repository.Connection {
	psd := `{"baseUrl":"` + base + `","clientId":"APP-ID"`
	if secret != "" {
		psd += `,"clientSecret":"` + secret + `"`
	}
	return connectionWith("gitlab", psd+`}`)
}

func TestAGitLabAccountRenewsWithItsOwnApplication(t *testing.T) {
	var body string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := make([]byte, 4096)
		n, _ := r.Body.Read(raw)
		body = string(raw[:n])
		_, _ = w.Write([]byte(`{"access_token":"NEW","refresh_token":"RT2","expires_in":7200}`))
	}))
	defer srv.Close()
	reg, err := upstream.Default()
	if err != nil {
		t.Fatal(err)
	}
	r := &Refresher{
		Flow: func(p string) (Flow, bool) {
			f, err := FlowFor(reg, func(string) string { return "" }, nil, p)
			return f, err == nil
		},
	}
	f, ok := r.FlowOf(gitlabConn(srv.URL, "APP-SECRET"))
	if !ok || f.ClientID != "APP-ID" || f.ClientSecret != "APP-SECRET" || f.TokenURL != srv.URL+"/oauth/token" {
		t.Fatalf("%+v %v", f, ok)
	}
	c := &TokenClient{HTTP: srv.Client()}
	if _, err := c.Refresh(context.Background(), f, "RT", nil); err != nil {
		t.Fatal(err)
	}
	form, _ := url.ParseQuery(body)
	if form.Get("client_secret") != "APP-SECRET" || form.Get("client_id") != "APP-ID" || form.Get("refresh_token") != "RT" {
		t.Fatalf("%v", form)
	}
	if _, ok := r.FlowOf(gitlabConn("http://evil.example", "")); ok {
		t.Fatal("an account with a bad server address was renewed")
	}
	if _, ok := r.FlowOf(connectionWith("gitlab", `{}`)); ok {
		t.Fatal("an account with no application was renewed")
	}
}

func TestAClineLoginHasNoPKCEAndSendsNoClientID(t *testing.T) {
	reg, err := upstream.Default()
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{Registry: reg, ClientID: func(string) string { return "" }, Sessions: NewSessions()}
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
