package muse

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"rllmx/app/llm/oauth"
	"rllmx/app/llm/upstream"
	"rllmx/tests/login"
	"rllmx/tests/store"
)

// newLoginRig is the shared rig with this package's steps.
func newLoginRig(t *testing.T, id, block string) *login.Login {
	t.Helper()
	return login.NewLogin(t, id, block, oauth.Dialects{Hooks: map[string]oauth.Hook{"muse": Account}})
}

// newRefreshRig is the renewal on that rig.
func newRefreshRig(t *testing.T, id, block string) (*login.Login, *oauth.Refresher, *store.Store) {
	t.Helper()
	r := newLoginRig(t, id, block)
	rf, st := login.NewRefresher(t, r, oauth.Dialects{Hooks: map[string]oauth.Hook{"muse": Account}})
	return r, rf, st
}

// FuzzMuseKeyAnswer does the same for the Meta key mint: an accepted login holds
// a bounded key and nothing the provider wrote as an address with a space.
func FuzzMuseKeyAnswer(f *testing.F) {
	for _, s := range []string{`{}`, `[]`, `{"api_key":"k"}`, `{"api_key":"k","is_subs_active":false}`, `{"api_key":"k","user_email":" A@B.C "}`, `{"api_key":"k","require_payment":true}`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) }))
		defer srv.Close()
		fl := oauth.Flow{OAuth: upstream.OAuth{Endpoints: map[string]string{upstream.EndpointKey: srv.URL}}}
		acc, err := Account(context.Background(), &oauth.TokenClient{HTTP: srv.Client()}, fl, oauth.Tokens{Access: "META"}, nil)
		if err != nil {
			if acc.Access != "" {
				t.Fatal("a failed hook returned a credential")
			}
			return
		}
		if acc.Access == "" || len(acc.Access) > oauth.MaxCredential || strings.ContainsAny(acc.Access, "\x00\r\n") || !acc.NoRefresh {
			t.Fatalf("accepted %+v", acc)
		}
	})
}

// A hook that fails ends the login and stores nothing.
func TestAMuseLoginTradesTheMetaTokenForTheKeyAndKeepsOnlyTheKey(t *testing.T) {
	r := newLoginRig(t, "mus", login.DeviceBlock+`,"hook":"muse","endpoints":{"key":"@/key"},"callHeaders":{"x-api-version":"1.0.0"}}`)
	r.F.On("/device", login.OK(`{"device_code":"DC","verification_uri":"https://v.example/x"}`))
	r.F.On("/token", login.OK(`{"access_token":"META-TOKEN","refresh_token":"META-RT","expires_in":99}`))
	r.F.On("/key", login.OK(`{"api_key":"LLM|KEY","user_email":"  Person@X.Test ","subs_tier_name":"pro","is_subs_active":true}`))
	if p := r.Run(t, "mus"); p.Status != "done" {
		t.Fatalf("%+v", p)
	}
	in := r.Accounts.Made(t)
	if in.Fields["accessToken"] != `"LLM|KEY"` || in.Email != "person@x.test" {
		t.Fatalf("%+v", in)
	}
	for _, k := range []string{"refreshToken", "expiresAt", "expiresIn"} {
		if _, has := in.Fields[k]; has {
			t.Errorf("%s was stored for a key that cannot be renewed", k)
		}
	}
	all := in.PSD
	for _, f := range in.Fields {
		all += f
	}
	if strings.Contains(all, "META-TOKEN") || strings.Contains(all, "META-RT") {
		t.Fatalf("the Meta token was stored: %s", all)
	}
	if login.PSD(t, in)["subscriptionTier"] != "pro" {
		t.Fatalf("%s", in.PSD)
	}
	hit := r.F.Last("/key")
	if hit.Header.Get("Authorization") != "Bearer META-TOKEN" || hit.Header.Get("X-Api-Version") != "1.0.0" || hit.Body != `{"onboard":true}` {
		t.Fatalf("%+v", hit)
	}
}

func TestAMuseKeyThatCannotBeHadEndsTheLoginWithAFixedWord(t *testing.T) {
	for name, key := range map[string][]login.Reply{
		"no key":            {login.OK(`{"user_email":"a@b.c"}`)},
		"an inactive plan":  {login.OK(`{"api_key":"K","is_subs_active":false}`)},
		"a payment demand":  {login.OK(`{"require_payment":true,"action_url":"https://pay.example"}`)},
		"a client error":    {{Status: 403, Body: `{"title":"SECRET"}`}},
		"not json":          {login.OK(`<html>SECRET`)},
		"a key with a byte": {login.OK("{\"api_key\":\"a\\u0000b\"}")},
		"a huge key":        {login.OK(`{"api_key":"` + strings.Repeat("k", oauth.MaxCredential+1) + `"}`)},
		"always busy":       {{Status: 429, Body: `x`}},
		"always failing":    {{Status: 503, Body: `x`}},
	} {
		t.Run(name, func(t *testing.T) {
			r := newLoginRig(t, "mus", login.DeviceBlock+`,"hook":"muse","endpoints":{"key":"@/key"}}`)
			r.F.On("/device", login.OK(`{"device_code":"DC","verification_uri":"https://v.example/x"}`))
			r.F.On("/token", login.OK(`{"access_token":"META-TOKEN"}`))
			r.F.On("/key", key...)
			p := r.Run(t, "mus")
			if p.Status != "error" || strings.Contains(p.Error, "SECRET") || len(r.Accounts.All()) != 0 {
				t.Fatalf("%+v, %d accounts", p, len(r.Accounts.All()))
			}
		})
	}
}

func TestAMuseKeyIsTriedAgainOnABusyAnswerWithGrowingPauses(t *testing.T) {
	r := newLoginRig(t, "mus", login.DeviceBlock+`,"hook":"muse","endpoints":{"key":"@/key"}}`)
	r.F.On("/device", login.OK(`{"device_code":"DC","verification_uri":"https://v.example/x"}`))
	r.F.On("/token", login.OK(`{"access_token":"META-TOKEN"}`))
	r.F.On("/key", login.Reply{Status: 429, Body: `x`}, login.Reply{Status: 503, Body: `x`}, login.OK(`{"api_key":"K"}`))
	if p := r.Run(t, "mus"); p.Status != "done" {
		t.Fatalf("%+v", p)
	}
	if r.F.Count("/key") != 3 || len(r.Waits) != 2 || r.Waits[0] != 5*time.Second || r.Waits[1] != 10*time.Second {
		t.Fatalf("%d calls, waits %v", r.F.Count("/key"), r.Waits)
	}
}

func TestAMuseRetryEndsWhenTheRequestDoes(t *testing.T) {
	r := newLoginRig(t, "mus", login.DeviceBlock+`,"hook":"muse","endpoints":{"key":"@/key"}}`)
	r.Service.Tokens.Sleep = func(context.Context, time.Duration) error { return context.Canceled }
	r.F.On("/device", login.OK(`{"device_code":"DC","verification_uri":"https://v.example/x"}`))
	r.F.On("/token", login.OK(`{"access_token":"META-TOKEN"}`))
	r.F.On("/key", login.Reply{Status: 429, Body: `x`})
	p := r.Run(t, "mus")
	if p.Status != "error" || r.F.Count("/key") != 1 || len(r.Accounts.All()) != 0 {
		t.Fatalf("%+v, %d calls", p, r.F.Count("/key"))
	}
}

func TestALoginThatCannotBeRenewedIsNeverDueAndNeverMarkedDead(t *testing.T) {
	r, rf, st := newRefreshRig(t, "mus", login.DeviceBlock+`,"hook":"muse","endpoints":{"key":"@/key"}}`)
	c := login.Seed(t, st, "mus", map[string]string{"accessToken": `"KEY"`, "testStatus": `"active"`}, "")
	if _, err := rf.Refresh(context.Background(), c.ID, ""); err != oauth.ErrNoRefresh {
		t.Fatalf("%v", err)
	}
	if refreshed, err := rf.Ensure(context.Background(), c.ID); err != nil || refreshed {
		t.Fatalf("%v %v", refreshed, err)
	}
	if n := rf.Pass(context.Background(), 0); n != 0 {
		t.Fatalf("a pass renewed %d", n)
	}
	got, _, _ := st.Connections().Get(context.Background(), c.ID)
	if s, _ := got.Doc().GetString("testStatus"); s != "active" {
		t.Fatalf("%q", s)
	}
	if r.F.Count("/token") != 0 {
		t.Fatal("the provider was called")
	}
}
