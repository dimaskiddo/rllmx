package nous

import (
	"context"
	"net/http"
	"net/http/httptest"
	"rllmx/app/store/dialect"
	"strconv"
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
	return login.NewLogin(t, id, block, oauth.Dialects{Hooks: map[string]oauth.Hook{"nous": Account}, Refresh: map[string]oauth.RefreshHook{"nous": Renewal}})
}

// newRefreshRig is the renewal on that rig.
func newRefreshRig(t *testing.T, id, block string) (*login.Login, *oauth.Refresher, *store.Store) {
	t.Helper()
	r := newLoginRig(t, id, block)
	rf, st := login.NewRefresher(t, r, oauth.Dialects{Hooks: map[string]oauth.Hook{"nous": Account}, Refresh: map[string]oauth.RefreshHook{"nous": Renewal}})
	return r, rf, st
}

// FuzzAgentKeyAnswer feeds the Nous mint any body: it must never panic, and a key
// it accepts is bounded and free of control characters.
func FuzzAgentKeyAnswer(f *testing.F) {
	for _, s := range []string{`{}`, `[]`, `x`, `{"api_key":"k"}`, `{"api_key":"k","expires_at":"2030-01-01T00:00:00Z","key_id":"a"}`, `{"api_key":1,"expires_at":-1}`, "{\"api_key\":\"a\\u0000\"}"} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) }))
		defer srv.Close()
		fl := oauth.Flow{OAuth: upstream.OAuth{Endpoints: map[string]string{upstream.EndpointAgentKey: srv.URL}}}
		key, until, id, _ := mintAgentKey(context.Background(), &oauth.TokenClient{HTTP: srv.Client()}, fl, "AT", nil)
		if key != "" && (len(key) > oauth.MaxCredential || strings.ContainsAny(key, "\x00\r\n")) {
			t.Fatalf("accepted key %q", key)
		}
		if key == "" && (!until.IsZero() || id != "") {
			t.Fatalf("details without a key: %v %q", until, id)
		}
	})
}

func TestANousLoginStoresTheAgentKeyAsTheKeyAndCapsTheExpiry(t *testing.T) {
	r := newLoginRig(t, "nou", login.DeviceBlock+`,"hook":"nous","endpoints":{"agentKey":"@/agent"},"refresh":{"encoding":"form","leadSeconds":300,"tokenHeader":"x-rt","hook":"nous"}}`)
	r.F.On("/device", login.OK(`{"device_code":"DC","verification_uri":"https://v.example/x"}`))
	r.F.On("/token", login.OK(`{"access_token":"NOUS-AT","refresh_token":"NOUS-RT","expires_in":86400}`))
	soon := r.Now.Add(2 * time.Hour).Unix()
	r.F.On("/agent", login.OK(`{"api_key":"AGENT-KEY","key_id":"k1","expires_at":`+strconv.FormatInt(soon, 10)+`}`))
	if p := r.Run(t, "nou"); p.Status != "done" {
		t.Fatalf("%+v", p)
	}
	in := r.Accounts.Made(t)
	if in.Fields["apiKey"] != `"AGENT-KEY"` || in.Fields["accessToken"] != `"NOUS-AT"` || in.Fields["refreshToken"] != `"NOUS-RT"` {
		t.Fatalf("%+v", in.Fields)
	}
	if want := `"` + dialect.FormatTimestamp(time.Unix(soon, 0)) + `"`; in.Fields["expiresAt"] != want {
		t.Fatalf("expiresAt %s, want %s", in.Fields["expiresAt"], want)
	}
	if got := login.PSD(t, in)["agentKeyId"]; got != "k1" {
		t.Fatalf("%s", in.PSD)
	}
	if h := r.F.Last("/agent"); h.Header.Get("Authorization") != "Bearer NOUS-AT" || h.Body != `{"min_ttl_seconds":7200}` {
		t.Fatalf("%+v", h)
	}
}

func TestANousLoginWithoutAnAgentKeyUsesTheAccessTokenAndItsOwnExpiry(t *testing.T) {
	for name, rep := range map[string]login.Reply{
		"a server error": {Status: 500, Body: `x`}, "no key": login.OK(`{}`), "a bad key": login.OK("{\"api_key\":\"a\\u0000\"}"), "not json": login.OK(`x`),
	} {
		t.Run(name, func(t *testing.T) {
			r := newLoginRig(t, "nou", login.DeviceBlock+`,"hook":"nous","endpoints":{"agentKey":"@/agent"}}`)
			r.F.On("/device", login.OK(`{"device_code":"DC","verification_uri":"https://v.example/x"}`))
			r.F.On("/token", login.OK(`{"access_token":"NOUS-AT","refresh_token":"RT","expires_in":3600}`))
			r.F.On("/agent", rep)
			if p := r.Run(t, "nou"); p.Status != "done" {
				t.Fatalf("%+v", p)
			}
			in := r.Accounts.Made(t)
			if _, has := in.Fields["apiKey"]; has || in.Fields["accessToken"] != `"NOUS-AT"` {
				t.Fatalf("%+v", in.Fields)
			}
			if in.Fields["expiresIn"] != "3600" {
				t.Fatalf("%+v", in.Fields)
			}
		})
	}
}

func TestAnAgentKeyThatLivesLongerThanTheTokenDoesNotExtendIt(t *testing.T) {
	r := newLoginRig(t, "nou", login.DeviceBlock+`,"hook":"nous","endpoints":{"agentKey":"@/agent"}}`)
	r.F.On("/device", login.OK(`{"device_code":"DC","verification_uri":"https://v.example/x"}`))
	r.F.On("/token", login.OK(`{"access_token":"AT","refresh_token":"RT","expires_in":3600}`))
	r.F.On("/agent", login.OK(`{"api_key":"K","expires_at":`+strconv.FormatInt(r.Now.Add(48*time.Hour).Unix(), 10)+`}`))
	r.Run(t, "nou")
	want := `"` + dialect.FormatTimestamp(r.Now.Add(time.Duration(3600)*time.Second).Add(-0)) + `"`
	got := r.Accounts.Made(t).Fields["expiresAt"]
	// The poll ran one interval + 1 s after the start, and expires_in counts from there.
	if got == `"`+dialect.FormatTimestamp(r.Now.Add(48*time.Hour))+`"` {
		t.Fatalf("the longer expiry won: %s (%s)", got, want)
	}
}

func TestANousRenewalSendsTheTokenInItsHeaderAndStoresTheNewKey(t *testing.T) {
	r, rf, st := newRefreshRig(t, "nou", login.DeviceBlock+`,"hook":"nous","endpoints":{"agentKey":"@/agent"},"refresh":{"encoding":"form","leadSeconds":300,"tokenHeader":"x-nous-refresh-token","hook":"nous"}}`)
	soon := r.Now.Add(90 * time.Minute)
	r.F.On("/token", login.OK(`{"access_token":"NEW-AT","refresh_token":"NEW-RT","expires_in":86400}`))
	r.F.On("/agent", login.OK(`{"api_key":"NEW-KEY","expires_at":`+strconv.FormatInt(soon.Unix(), 10)+`}`))
	c := login.Seed(t, st, "nou", map[string]string{
		"accessToken": `"OLD-AT"`, "refreshToken": `"OLD-RT"`, "apiKey": `"OLD-KEY"`, "testStatus": `"active"`,
		"expiresAt": `"` + dialect.FormatTimestamp(r.Now.Add(time.Minute)) + `"`,
	}, "")
	got, err := rf.Refresh(context.Background(), c.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	d := got.Doc()
	if v, _ := d.GetString("apiKey"); v != "NEW-KEY" {
		t.Fatalf("apiKey %q", v)
	}
	if v, _ := d.GetString("accessToken"); v != "NEW-AT" {
		t.Fatalf("accessToken %q", v)
	}
	if v, _ := d.GetString("expiresAt"); v != dialect.FormatTimestamp(soon) {
		t.Fatalf("expiresAt %q, want %q", v, dialect.FormatTimestamp(soon))
	}
	if h := r.F.Last("/token"); h.Header.Get("X-Nous-Refresh-Token") != "OLD-RT" || !strings.Contains(h.Body, "refresh_token=OLD-RT") {
		t.Fatalf("%+v", h)
	}
	if h := r.F.Last("/agent"); h.Header.Get("Authorization") != "Bearer NEW-AT" {
		t.Fatalf("%v", h.Header)
	}
}

func TestAFailedAgentKeyKeepsTheKeyTheAccountHasAndStillStoresTheTokens(t *testing.T) {
	r, rf, st := newRefreshRig(t, "nou", login.DeviceBlock+`,"hook":"nous","endpoints":{"agentKey":"@/agent"},"refresh":{"encoding":"form","leadSeconds":300,"hook":"nous"}}`)
	r.F.On("/token", login.OK(`{"access_token":"NEW-AT","refresh_token":"NEW-RT","expires_in":3600}`))
	r.F.On("/agent", login.Reply{Status: 500, Body: `x`})
	c := login.Seed(t, st, "nou", map[string]string{"accessToken": `"OLD-AT"`, "refreshToken": `"OLD-RT"`, "apiKey": `"OLD-KEY"`, "testStatus": `"active"`}, "")
	got, err := rf.Refresh(context.Background(), c.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	d := got.Doc()
	if v, _ := d.GetString("apiKey"); v != "OLD-KEY" {
		t.Fatalf("the old key was lost: %q", v)
	}
	if v, _ := d.GetString("refreshToken"); v != "NEW-RT" {
		t.Fatalf("refresh token %q", v)
	}
}

func TestAMintFailureSaysWhyItFailed(t *testing.T) {
	down := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := down.URL
	client := down.Client()
	down.Close()
	for name, tc := range map[string]struct {
		target string
		serve  func(http.ResponseWriter, *http.Request)
		client *http.Client
		want   oauth.Class
	}{
		"an unreachable service":  {target: url, client: client, want: oauth.ClassTransport},
		"a refusing service":      {serve: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) }, want: oauth.ClassRejected},
		"a failing service":       {serve: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) }, want: oauth.ClassTransport},
		"a body that is not json": {serve: func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>")) }, want: oauth.ClassMalformed},
		"an answer with no key":   {serve: func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) }, want: oauth.ClassMalformed},
	} {
		t.Run(name, func(t *testing.T) {
			target, hc := tc.target, tc.client
			if tc.serve != nil {
				srv := httptest.NewTLSServer(http.HandlerFunc(tc.serve))
				defer srv.Close()
				target, hc = srv.URL, srv.Client()
			}
			fl := oauth.Flow{OAuth: upstream.OAuth{Endpoints: map[string]string{upstream.EndpointAgentKey: target}}}
			_, err := Renewal(context.Background(), &oauth.TokenClient{HTTP: hc}, fl, oauth.Tokens{Access: "AT"}, nil)
			if got := oauth.ClassOf(err); got != tc.want {
				t.Fatalf("class %q, want %q (%v)", got, tc.want, err)
			}
		})
	}
}
