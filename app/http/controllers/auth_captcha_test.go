package controllers

import (
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"rllmx/app/access/captcha"
)

// challengeOn gives the rig a verifier that answers whatever the test's server
// says.
func challengeOn(t *testing.T, r *authRig, answer func(w nethttp.ResponseWriter)) {
	t.Helper()
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		answer(w)
	}))
	t.Cleanup(srv.Close)
	r.ctl.Captcha = &captcha.Verifier{Provider: captcha.Turnstile, URL: srv.URL, Client: srv.Client()}
	r.ctl.CaptchaKey = "site-key"
}

const loginBody = `{"username":"admin","password":"123456"}`

func TestARemoteLoginWithoutAChallengeAnswerIsRefused(t *testing.T) {
	r := newAuthRig(t)
	challengeOn(t, r, func(w nethttp.ResponseWriter) { _, _ = w.Write([]byte(`{"success":true}`)) })
	rec := r.callAs(nethttp.MethodPost, "/api/auth/login", loginBody, nil, remotePeer, remoteHost)
	if rec.Code != nethttp.StatusForbidden || !strings.Contains(rec.Body.String(), `"captcha":true`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestARemoteLoginWithAGoodAnswerReachesThePasswordCheck(t *testing.T) {
	r := newAuthRig(t)
	challengeOn(t, r, func(w nethttp.ResponseWriter) { _, _ = w.Write([]byte(`{"success":true}`)) })
	rec := r.callAs(nethttp.MethodPost, "/api/auth/login", `{"username":"admin","password":"123456","captcha":"ok"}`, nil, remotePeer, remoteHost)
	// The public default is refused remotely, which is a later gate than the
	// challenge: reaching it proves the answer passed.
	if rec.Code != nethttp.StatusForbidden || !strings.Contains(rec.Body.String(), `"mustChangePassword":true`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestALocalLoginSkipsTheChallenge(t *testing.T) {
	r := newAuthRig(t)
	challengeOn(t, r, func(w nethttp.ResponseWriter) { _, _ = w.Write([]byte(`{"success":false}`)) })
	rec := r.call(nethttp.MethodPost, "/api/auth/login", loginBody, nil)
	if rec.Code != nethttp.StatusOK {
		t.Fatalf("the machine was asked to pass a challenge: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAnUnreachableChallengeProviderFailsClosedWithoutCounting(t *testing.T) {
	r := newAuthRig(t)
	challengeOn(t, r, func(w nethttp.ResponseWriter) { w.WriteHeader(nethttp.StatusBadGateway) })
	rec := r.callAs(nethttp.MethodPost, "/api/auth/login", `{"username":"admin","password":"123456","captcha":"ok"}`, nil, remotePeer, remoteHost)
	if rec.Code != nethttp.StatusServiceUnavailable {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"captcha":true`) {
		t.Fatalf("an unreachable provider read as a refused answer: %s", rec.Body.String())
	}
}

func TestTheStatusCarriesTheChallengeToARemoteCallerOnly(t *testing.T) {
	r := newAuthRig(t)
	challengeOn(t, r, func(w nethttp.ResponseWriter) { _, _ = w.Write([]byte(`{"success":true}`)) })
	remote := r.callAs(nethttp.MethodGet, "/api/auth/status", "", nil, remotePeer, remoteHost)
	if !strings.Contains(remote.Body.String(), `"siteKey":"site-key"`) || !strings.Contains(remote.Body.String(), "challenges.cloudflare.com") {
		t.Fatalf("remote status %s", remote.Body.String())
	}
	local := r.call(nethttp.MethodGet, "/api/auth/status", "", nil)
	if strings.Contains(local.Body.String(), `"captcha"`) {
		t.Fatalf("the machine was offered a challenge: %s", local.Body.String())
	}
}

func TestNoChallengeProviderLeavesTheFormAsItWas(t *testing.T) {
	r := newAuthRig(t)
	remote := r.callAs(nethttp.MethodPost, "/api/auth/login", loginBody, nil, remotePeer, remoteHost)
	if !strings.Contains(remote.Body.String(), `"mustChangePassword":true`) {
		t.Fatalf("%d %s", remote.Code, remote.Body.String())
	}
}
