package oauth_test

import (
	"context"
	"rllmx/app/llm/oauth"
	"rllmx/tests/login"
	"strings"
	"testing"
	"time"

	"rllmx/app/store/dialect"
	"rllmx/app/store/repository"
	"rllmx/tests/store"
)

const deviceBlock = login.DeviceBlock

func TestCallHeadersCarryTheAccountsDeviceIDOnEveryCall(t *testing.T) {
	r := login.NewLogin(t, "kim", deviceBlock+`,"callHeaders":{"X-Dev":"{deviceId}","X-Fixed":"f"},"refresh":{"encoding":"form","leadSeconds":300}}`, oauth.Dialects{})
	r.F.On("/device", login.OK(`{"device_code":"DC","user_code":"UC","verification_uri":"https://v.example/x"}`))
	r.F.On("/token", login.OK(`{"access_token":"AT","refresh_token":"RT","expires_in":3600}`))
	if p := r.Run(t, "kim"); p.Status != "done" {
		t.Fatalf("%+v", p)
	}
	start, poll := r.F.Last("/device"), r.F.Last("/token")
	id := start.Header.Get("X-Dev")
	if id == "" || poll.Header.Get("X-Dev") != id || start.Header.Get("X-Fixed") != "f" {
		t.Fatalf("start %v poll %v", start.Header, poll.Header)
	}
	if got := login.PSD(t, r.Accounts.Made(t))["deviceId"]; got != id {
		t.Fatalf("stored device id %v, sent %q", got, id)
	}
	if strings.Contains(start.Body+poll.Body, id) {
		t.Fatal("the device id went in a body")
	}
}

func TestADeviceIDIsMadeOnlyWhenTheDataAsksForOne(t *testing.T) {
	r := login.NewLogin(t, "plain", deviceBlock+`}`, oauth.Dialects{})
	r.F.On("/device", login.OK(`{"device_code":"DC","verification_uri":"https://v.example/x"}`))
	r.F.On("/token", login.OK(`{"access_token":"AT"}`))
	if p := r.Run(t, "plain"); p.Status != "done" {
		t.Fatalf("%+v", p)
	}
	if _, has := login.PSD(t, r.Accounts.Made(t))["deviceId"]; has {
		t.Fatal("a device id was stored with no header asking for it")
	}
}

func TestTwoLoginsGetTwoDeviceIDs(t *testing.T) {
	r := login.NewLogin(t, "kim", deviceBlock+`,"callHeaders":{"X-Dev":"{deviceId}"}}`, oauth.Dialects{})
	r.F.On("/device", login.OK(`{"device_code":"DC","verification_uri":"https://v.example/x"}`))
	r.F.On("/token", login.OK(`{"access_token":"AT"}`))
	a, _ := r.Service.StartDevice(context.Background(), "kim", "bind")
	first := r.F.Last("/device").Header.Get("X-Dev")
	b, _ := r.Service.StartDevice(context.Background(), "kim", "bind")
	second := r.F.Last("/device").Header.Get("X-Dev")
	if a.State == b.State || first == "" || first == second {
		t.Fatalf("ids %q %q", first, second)
	}
}

func TestAStartAnswerWithNoPageFallsBackToTheDataAddress(t *testing.T) {
	r := login.NewLogin(t, "kim", deviceBlock+`,"endpoints":{"verify":"https://v.example/authorize"}}`, oauth.Dialects{})
	r.F.On("/device", login.OK(`{"device_code":"DC","user_code":"AB 12"}`))
	s, err := r.Service.StartDevice(context.Background(), "kim", "bind")
	if err != nil || s.VerificationURI != "https://v.example/authorize" {
		t.Fatalf("a user code with a space: %+v %v", s, err)
	}
	r.F.On("/device", login.OK(`{"device_code":"DC","user_code":"AB-12"}`))
	s, err = r.Service.StartDevice(context.Background(), "kim", "bind")
	if err != nil || s.VerificationURI != "https://v.example/authorize?user_code=AB-12" {
		t.Fatalf("%+v %v", s, err)
	}
	r.F.On("/device", login.OK(`{"device_code":"DC","verification_uri":"https://own.example/p"}`))
	s, _ = r.Service.StartDevice(context.Background(), "kim", "bind")
	if s.VerificationURI != "https://own.example/p" {
		t.Fatalf("the provider's own page lost: %+v", s)
	}
}

func TestAClientIDIsStillRequiredWhenTheDataDoesNotWaiveIt(t *testing.T) {
	r := login.NewLogin(t, "plain", deviceBlock+`}`, oauth.Dialects{})
	r.Service.ClientID = func(string) string { return "" }
	if _, err := r.Service.StartDevice(context.Background(), "plain", "bind"); err != oauth.ErrDisabled {
		t.Fatalf("%v", err)
	}
}

// refreshRig renews an account of a device provider against the fake.
func newRefreshRig(t *testing.T, id, block string) (*login.Login, *oauth.Refresher, *store.Store) {
	t.Helper()
	r := login.NewLogin(t, id, block, oauth.Dialects{Hooks: map[string]oauth.Hook{"kimchi": nil}})
	rf, st := login.NewRefresher(t, r, oauth.Dialects{})
	return r, rf, st
}

func seed(t *testing.T, st *store.Store, provider string, fields map[string]string, psd string) repository.Connection {
	t.Helper()
	c, err := st.Connections().Create(context.Background(), repository.ConnInput{Provider: provider, AuthType: "oauth", Email: "a@x.test", Fields: fields, PSD: psd}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestAKimiRenewalCarriesItsHeadersWithTheStoredDeviceID(t *testing.T) {
	r, rf, st := newRefreshRig(t, "kim", deviceBlock+`,"callHeaders":{"X-Msh-Device-Id":"{deviceId}","X-Msh-Platform":"p"},"refresh":{"encoding":"form","leadSeconds":300}}`)
	r.F.On("/token", login.OK(`{"access_token":"NEW-AT","refresh_token":"NEW-RT","expires_in":3600}`))
	c := seed(t, st, "kim", map[string]string{
		"accessToken": `"OLD"`, "refreshToken": `"OLD-RT"`, "testStatus": `"active"`,
		"expiresAt": `"` + dialect.FormatTimestamp(r.Now.Add(time.Minute)) + `"`,
	}, `{"deviceId":"dev-42"}`)
	if _, err := rf.Refresh(context.Background(), c.ID, ""); err != nil {
		t.Fatal(err)
	}
	h := r.F.Last("/token")
	if h.Header.Get("X-Msh-Device-Id") != "dev-42" || h.Header.Get("X-Msh-Platform") != "p" {
		t.Fatalf("%v", h.Header)
	}
}

func TestAnAccountWithNoDeviceIDSendsNoDeviceIDHeader(t *testing.T) {
	r, rf, st := newRefreshRig(t, "kim", deviceBlock+`,"callHeaders":{"X-Msh-Device-Id":"{deviceId}","X-Msh-Platform":"p"},"refresh":{"encoding":"form","leadSeconds":300}}`)
	r.F.On("/token", login.OK(`{"access_token":"NEW-AT"}`))
	c := seed(t, st, "kim", map[string]string{"accessToken": `"OLD"`, "refreshToken": `"OLD-RT"`, "testStatus": `"active"`}, "")
	if _, err := rf.Refresh(context.Background(), c.ID, ""); err != nil {
		t.Fatal(err)
	}
	h := r.F.Last("/token")
	if _, has := h.Header["X-Msh-Device-Id"]; has || h.Header.Get("X-Msh-Platform") != "p" {
		t.Fatalf("%v", h.Header)
	}
}

func TestAnInvalidGrantStillMarksADeviceAccountDead(t *testing.T) {
	r, rf, st := newRefreshRig(t, "kim", deviceBlock+`,"refresh":{"encoding":"form","leadSeconds":300}}`)
	r.F.On("/token", login.Reply{Status: 400, Body: `{"error":"invalid_grant"}`})
	c := seed(t, st, "kim", map[string]string{"accessToken": `"OLD"`, "refreshToken": `"OLD-RT"`, "testStatus": `"active"`}, "")
	if _, err := rf.Refresh(context.Background(), c.ID, ""); err != oauth.ErrExpired {
		t.Fatalf("%v", err)
	}
}
