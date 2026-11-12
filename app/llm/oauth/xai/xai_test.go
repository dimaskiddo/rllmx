package xai

import (
	"encoding/base64"
	"strings"
	"testing"

	"rllmx/app/llm/oauth"
	"rllmx/tests/login"
)

// newLoginRig is the shared rig with this package's steps.
func newLoginRig(t *testing.T, id, block string) *login.Login {
	t.Helper()
	return login.NewLogin(t, id, block, oauth.Dialects{Hooks: map[string]oauth.Hook{"grokCli": Grok, "xai": Xai}})
}

func TestExtraStartParametersAreSentAndTheClientIDIsNotDuplicated(t *testing.T) {
	r := newLoginRig(t, "grk", login.DeviceBlock+`,"params":[{"name":"referrer","value":"grok-build"}]}`)
	r.F.On("/device", login.OK(`{"device_code":"DC","verification_uri":"https://v.example/x"}`))
	r.F.On("/token", login.OK(`{"access_token":"AT"}`))
	r.Run(t, "grk")
	body := r.F.Last("/device").Body
	for _, want := range []string{"referrer=grok-build", "client_id=client-grk", "scope=s"} {
		if !strings.Contains(body, want) {
			t.Errorf("start body %q lacks %q", body, want)
		}
	}
}

func TestAGrokLoginNamesTheAccountFromTheTokensAndNeverFromTheSubject(t *testing.T) {
	r := newLoginRig(t, "grk", login.DeviceBlock+`,"params":[{"name":"referrer","value":"grok-build"}],"hook":"grokCli","endpoints":{"user":"@/user"},"callHeaders":{"x-grok-client-version":"1.0.44"}}`)
	r.F.On("/device", login.OK(`{"device_code":"DC","verification_uri":"https://v.example/x"}`))
	id := jwtFor(`{"sub":"subject-id-1234"}`)
	r.F.On("/token", login.OK(`{"access_token":"`+jwtFor(`{"sub":"subject-id-1234"}`)+`","refresh_token":"RT","id_token":"`+id+`","expires_in":600}`))
	r.F.On("/user", login.OK(`{"email":"profile@x.test","userId":"U-1","subscriptionTier":"super","hasGrokCodeAccess":true}`))
	if p := r.Run(t, "grk"); p.Status != "done" {
		t.Fatalf("%+v", p)
	}
	in := r.Accounts.Made(t)
	psd := login.PSD(t, in)
	if in.Email != "profile@x.test" || psd["email"] != "profile@x.test" || psd["userId"] != "U-1" || psd["subscriptionTier"] != "super" || psd["hasGrokCodeAccess"] != true {
		t.Fatalf("%+v %s", in, in.PSD)
	}
	if in.Fields["idToken"] == "" {
		t.Fatal("the id token is not stored at the top level")
	}
	if h := r.F.Last("/user"); h.Header.Get("X-Grok-Client-Version") != "1.0.44" || h.Header.Get("Authorization") == "" {
		t.Fatalf("%v", h.Header)
	}
}

func TestAGrokEmailComesFromTheIDTokenFirstThenTheAccessTokenThenTheProfile(t *testing.T) {
	cases := map[string]struct {
		id, access, profile, want string
	}{
		"the id token":            {`{"email":"id@x.test"}`, `{"email":"at@x.test"}`, `{"email":"p@x.test"}`, "id@x.test"},
		"its user name":           {`{"preferred_username":"name@x.test"}`, ``, `{}`, "name@x.test"},
		"the access token":        {`{"sub":"s"}`, `{"email":"at@x.test"}`, `{"email":"p@x.test"}`, "at@x.test"},
		"the profile":             {`{"sub":"s"}`, `{"sub":"s"}`, `{"email":"p@x.test"}`, "p@x.test"},
		"nothing but a subject":   {`{"sub":"only-a-subject"}`, `{"sub":"only-a-subject"}`, `{}`, ""},
		"an address with a space": {`{"email":"a b@x.test"}`, ``, `{}`, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := newLoginRig(t, "grk", login.DeviceBlock+`,"hook":"grokCli","endpoints":{"user":"@/user"}}`)
			r.F.On("/device", login.OK(`{"device_code":"DC","verification_uri":"https://v.example/x"}`))
			access := "plain-access"
			if tc.access != "" {
				access = jwtFor(tc.access)
			}
			r.F.On("/token", login.OK(`{"access_token":"`+access+`","id_token":"`+jwtFor(tc.id)+`"}`))
			r.F.On("/user", login.OK(tc.profile))
			if p := r.Run(t, "grk"); p.Status != "done" {
				t.Fatalf("%+v", p)
			}
			if got := r.Accounts.Made(t).Email; got != tc.want {
				t.Fatalf("email %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAGrokLoginStillConnectsWhenTheProfileFails(t *testing.T) {
	r := newLoginRig(t, "grk", login.DeviceBlock+`,"hook":"grokCli","endpoints":{"user":"@/user"}}`)
	r.F.On("/device", login.OK(`{"device_code":"DC","verification_uri":"https://v.example/x"}`))
	r.F.On("/token", login.OK(`{"access_token":"AT","refresh_token":"RT"}`))
	r.F.On("/user", login.Reply{Status: 500, Body: `x`})
	if p := r.Run(t, "grk"); p.Status != "done" {
		t.Fatalf("%+v", p)
	}
}

// jwtFor is a token whose middle part is the given JSON.
func jwtFor(payload string) string {
	return "h." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".s"
}
