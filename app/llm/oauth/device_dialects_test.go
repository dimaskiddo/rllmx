package oauth_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"rllmx/app/llm/oauth"
	"rllmx/app/llm/oauth/codebuddy"
	"rllmx/app/llm/oauth/kilocode"
	"rllmx/app/llm/oauth/minimax"
	"rllmx/app/llm/oauth/zcode"
	"rllmx/tests/login"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/access/oidc"
	"rllmx/app/llm/upstream"
)

const codebuddyBlock = `{"flow":"device","dialect":"codebuddy","noClient":true,"deviceUrl":"@/state","tokenUrl":"@/token","scope":"-",` +
	`"params":[{"name":"platform","value":"CLI"}],"callHeaders":{"X-Domain":"d.example"},"refresh":{"encoding":"json","leadSeconds":300,"url":"@/refresh"}}`

func TestACodeBuddyLoginPollsByStateAndWaitsOnTheBusyCode(t *testing.T) {
	r := login.NewLogin(t, "cb", codebuddyBlock, oauth.Dialects{Device: map[string]oauth.DeviceDialect{"codebuddy": {Start: codebuddy.Start, Poll: codebuddy.Poll, Refresh: codebuddy.Refresh}, "minimax": {Start: minimax.Start, Poll: minimax.Poll}, "zcode": {Start: zcode.Start, Poll: zcode.Poll}, "kilocode": {Start: kilocode.Start, Poll: kilocode.Poll}}})
	r.F.On("/state", login.OK(`{"code":0,"data":{"state":"ST ATE&1","authUrl":"https://auth.example/login?s=1"}}`))
	r.F.On("/token", login.OK(`{"code":11217,"msg":"RETRY"}`), login.OK(`{"code":0,"data":{"accessToken":"AT","refreshToken":"RT","expiresIn":7200}}`))
	s, err := r.Service.StartDevice(context.Background(), "cb", "bind")
	if err != nil || s.VerificationURI != "https://auth.example/login?s=1" || s.UserCode != "" || s.Interval != 5 {
		t.Fatalf("%+v %v", s, err)
	}
	start := r.F.Last("/state")
	if start.Query != "platform=CLI" || start.Body != "{}" || start.Header.Get("X-No-Authorization") != "true" || start.Header.Get("X-Domain") != "d.example" {
		t.Fatalf("start %+v", start)
	}
	r.Advance(6 * time.Second)
	if p := r.Service.PollDevice(context.Background(), s.State, "bind"); p.Status != "pending" {
		t.Fatalf("%+v", p)
	}
	r.Advance(6 * time.Second)
	p := r.Service.PollDevice(context.Background(), s.State, "bind")
	if p.Status != "done" {
		t.Fatalf("%+v", p)
	}
	poll := r.F.Last("/token")
	if poll.Method != "GET" || poll.Query != "state=ST+ATE%261" || poll.Header.Get("X-No-User-Id") != "true" {
		t.Fatalf("poll %+v", poll)
	}
	in := r.Accounts.Made(t)
	if in.Fields["accessToken"] != `"AT"` || in.Fields["refreshToken"] != `"RT"` || in.Fields["expiresIn"] != "7200" {
		t.Fatalf("%+v", in.Fields)
	}
}

func TestACodeBuddyAnswerIsCheckedAndNeverShown(t *testing.T) {
	for name, tc := range map[string]struct {
		reply login.Reply
		class oauth.Class
	}{
		"a refused code":     {login.OK(`{"code":500,"msg":"SECRET"}`), oauth.ClassMalformed},
		"no state":           {login.OK(`{"code":0,"data":{"authUrl":"https://a.example/x"}}`), oauth.ClassMalformed},
		"a plain http page":  {login.OK(`{"code":0,"data":{"state":"s","authUrl":"http://a.example/x"}}`), oauth.ClassMalformed},
		"a server error":     {login.Reply{Status: 500, Body: "SECRET"}, oauth.ClassTransport},
		"not an object":      {login.OK(`[]`), oauth.ClassMalformed},
		"a state with a NUL": {login.OK("{\"code\":0,\"data\":{\"state\":\"a\\u0000\",\"authUrl\":\"https://a.example/x\"}}"), oauth.ClassMalformed},
	} {
		t.Run(name, func(t *testing.T) {
			r := login.NewLogin(t, "cb", codebuddyBlock, oauth.Dialects{Device: map[string]oauth.DeviceDialect{"codebuddy": {Start: codebuddy.Start, Poll: codebuddy.Poll, Refresh: codebuddy.Refresh}, "minimax": {Start: minimax.Start, Poll: minimax.Poll}, "zcode": {Start: zcode.Start, Poll: zcode.Poll}, "kilocode": {Start: kilocode.Start, Poll: kilocode.Poll}}})
			r.F.On("/state", tc.reply)
			_, err := r.Service.StartDevice(context.Background(), "cb", "bind")
			if oauth.ClassOf(err) != tc.class {
				t.Fatalf("%v", err)
			}
		})
	}
}

func TestACodeBuddyPollEndsOnAnythingButTheBusyCode(t *testing.T) {
	for name, tc := range map[string]struct {
		reply login.Reply
		want  string
	}{
		"another code":        {login.OK(`{"code":1,"msg":"SECRET"}`), oauth.CodeRefused},
		"a server error":      {login.Reply{Status: 503, Body: "SECRET"}, "transport"},
		"zero without tokens": {login.OK(`{"code":0,"data":{}}`), oauth.CodeRefused},
		"no code":             {login.OK(`{"data":{"accessToken":"AT"}}`), oauth.CodeRefused},
	} {
		t.Run(name, func(t *testing.T) {
			r := login.NewLogin(t, "cb", codebuddyBlock, oauth.Dialects{Device: map[string]oauth.DeviceDialect{"codebuddy": {Start: codebuddy.Start, Poll: codebuddy.Poll, Refresh: codebuddy.Refresh}, "minimax": {Start: minimax.Start, Poll: minimax.Poll}, "zcode": {Start: zcode.Start, Poll: zcode.Poll}, "kilocode": {Start: kilocode.Start, Poll: kilocode.Poll}}})
			r.F.On("/state", login.OK(`{"code":0,"data":{"state":"s","authUrl":"https://a.example/x"}}`))
			r.F.On("/token", tc.reply)
			p := r.Run(t, "cb")
			if p.Status != "error" || p.Error != tc.want || strings.Contains(p.Error, "SECRET") {
				t.Fatalf("%+v", p)
			}
		})
	}
}

func TestACodeBuddyRenewalSendsTheRefreshTokenInAHeader(t *testing.T) {
	r := login.NewLogin(t, "cb", codebuddyBlock, oauth.Dialects{Device: map[string]oauth.DeviceDialect{"codebuddy": {Start: codebuddy.Start, Poll: codebuddy.Poll, Refresh: codebuddy.Refresh}, "minimax": {Start: minimax.Start, Poll: minimax.Poll}, "zcode": {Start: zcode.Start, Poll: zcode.Poll}, "kilocode": {Start: kilocode.Start, Poll: kilocode.Poll}}})
	f := flowOf(t, r, "cb", "")
	r.F.On("/refresh", login.OK(`{"code":0,"data":{"accessToken":"AT2","expiresIn":100}}`))
	tok, err := r.Service.Tokens.Refresh(context.Background(), f, "RT", oauth.CallHeaders(f, ""))
	if err != nil || tok.Access != "AT2" || tok.Refresh != "RT" || tok.ExpiresIn != 100 {
		t.Fatalf("%+v %v", tok, err)
	}
	h := r.F.Last("/refresh")
	if h.Header.Get("X-Refresh-Token") != "RT" || h.Header.Get("X-Auth-Refresh-Source") != "plugin" || h.Body != "{}" || h.Header.Get("X-Domain") != "d.example" {
		t.Fatalf("%+v", h)
	}
	for name, tc := range map[string]struct {
		reply login.Reply
		class oauth.Class
	}{
		"a 401 kills the sign-in":         {login.Reply{Status: 401, Body: "x"}, oauth.ClassInvalidGrant},
		"a refusal in the body is not":    {login.OK(`{"code":9,"msg":"invalid_grant"}`), oauth.ClassRejected},
		"a server error":                  {login.Reply{Status: 500, Body: "x"}, oauth.ClassTransport},
		"a code zero with no token":       {login.OK(`{"code":0,"data":{}}`), oauth.ClassMalformed},
		"an answer that is not an object": {login.OK(`[]`), oauth.ClassMalformed},
	} {
		r.F.On("/refresh", tc.reply)
		if _, err := r.Service.Tokens.Refresh(context.Background(), f, "RT", nil); oauth.ClassOf(err) != tc.class {
			t.Errorf("%s: %v", name, err)
		}
	}
}

const minimaxBlock = `{"flow":"device","dialect":"minimax","deviceUrl":"@/device","tokenUrl":"@/token","scope":"agent.default",` +
	`"params":[{"name":"audience","value":"agent-backend"}],"refresh":{"encoding":"form","leadSeconds":600,"fields":{"scope":"agent.default","audience":"agent-backend"}}}`

func formOf(t *testing.T, body string) url.Values {
	t.Helper()
	v, err := url.ParseQuery(body)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestAMiniMaxLoginSendsAChallengeAndProvesItOnThePoll(t *testing.T) {
	r := login.NewLogin(t, "mm", minimaxBlock, oauth.Dialects{Device: map[string]oauth.DeviceDialect{"codebuddy": {Start: codebuddy.Start, Poll: codebuddy.Poll, Refresh: codebuddy.Refresh}, "minimax": {Start: minimax.Start, Poll: minimax.Poll}, "zcode": {Start: zcode.Start, Poll: zcode.Poll}, "kilocode": {Start: kilocode.Start, Poll: kilocode.Poll}}})
	r.F.On("/device", login.OK(`{"device_code":"DC","user_code":"UC","verification_uri":"https://a.example/p","verification_uri_complete":"https://a.example/p?c=UC","expires_in":300,"interval":4}`))
	r.F.On("/token", login.OK(`{"access_token":"AT","refresh_token":"RT","expires_in":3600}`))
	s, err := r.Service.StartDevice(context.Background(), "mm", "bind")
	if err != nil || s.VerificationURI != "https://a.example/p?c=UC" || s.Interval != 4 || s.ExpiresIn != 300 || s.UserCode != "UC" {
		t.Fatalf("%+v %v", s, err)
	}
	start := formOf(t, r.F.Last("/device").Body)
	if start.Get("client_id") != "client-mm" || start.Get("scope") != "agent.default" || start.Get("audience") != "agent-backend" || start.Get("code_challenge_method") != "S256" {
		t.Fatalf("%v", start)
	}
	r.Advance(5 * time.Second)
	if p := r.Service.PollDevice(context.Background(), s.State, "bind"); p.Status != "done" {
		t.Fatalf("%+v", p)
	}
	poll := formOf(t, r.F.Last("/token").Body)
	if poll.Get("device_code") != "DC" || poll.Has("user_code") || poll.Get("grant_type") != "urn:ietf:params:oauth:grant-type:device_code" || oidc.Challenge(poll.Get("code_verifier")) != start.Get("code_challenge") {
		t.Fatalf("%v", poll)
	}
}

func TestAMiniMaxLoginWithoutADeviceCodePollsWithTheUserCode(t *testing.T) {
	r := login.NewLogin(t, "mm", minimaxBlock, oauth.Dialects{Device: map[string]oauth.DeviceDialect{"codebuddy": {Start: codebuddy.Start, Poll: codebuddy.Poll, Refresh: codebuddy.Refresh}, "minimax": {Start: minimax.Start, Poll: minimax.Poll}, "zcode": {Start: zcode.Start, Poll: zcode.Poll}, "kilocode": {Start: kilocode.Start, Poll: kilocode.Poll}}})
	r.F.On("/device", login.OK(`{"user_code":"UC","verification_uri":"https://a.example/p","expired_in":`+strconv.FormatInt(r.Now.Add(10*time.Minute).UnixMilli(), 10)+`,"interval":5000}`))
	r.F.On("/token", login.OK(`{"status":"pending"}`))
	s, err := r.Service.StartDevice(context.Background(), "mm", "bind")
	if err != nil || s.Interval != 5 || s.ExpiresIn < 590 || s.ExpiresIn > 600 {
		t.Fatalf("%+v %v", s, err)
	}
	r.Advance(6 * time.Second)
	if p := r.Service.PollDevice(context.Background(), s.State, "bind"); p.Status != "pending" {
		t.Fatalf("%+v", p)
	}
	poll := formOf(t, r.F.Last("/token").Body)
	if poll.Get("user_code") != "UC" || poll.Has("device_code") {
		t.Fatalf("%v", poll)
	}
}

func TestAMiniMaxPollReadsItsStatusWordsAndErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		reply  login.Reply
		status string
		code   string
	}{
		"pending":       {login.OK(`{"status":"pending"}`), "pending", ""},
		"slow down":     {login.OK(`{"status":"slow_down"}`), "pending", ""},
		"denied":        {login.OK(`{"status":"denied"}`), "error", oauth.CodeDenied},
		"expired":       {login.OK(`{"status":"expired"}`), "error", oauth.CodeExpired},
		"standard form": {login.Reply{Status: 400, Body: `{"error":"authorization_pending"}`}, "pending", ""},
		"standard deny": {login.Reply{Status: 400, Body: `{"error":"access_denied"}`}, "error", oauth.CodeDenied},
		"an unknown":    {login.OK(`{"status":"weird","error_description":"SECRET"}`), "error", oauth.CodeRefused},
		"no token":      {login.OK(`{"foo":1}`), "error", oauth.CodeRefused},
		"server error":  {login.Reply{Status: 502, Body: "SECRET"}, "error", "transport"},
	} {
		t.Run(name, func(t *testing.T) {
			r := login.NewLogin(t, "mm", minimaxBlock, oauth.Dialects{Device: map[string]oauth.DeviceDialect{"codebuddy": {Start: codebuddy.Start, Poll: codebuddy.Poll, Refresh: codebuddy.Refresh}, "minimax": {Start: minimax.Start, Poll: minimax.Poll}, "zcode": {Start: zcode.Start, Poll: zcode.Poll}, "kilocode": {Start: kilocode.Start, Poll: kilocode.Poll}}})
			r.F.On("/device", login.OK(`{"device_code":"DC","user_code":"UC","verification_uri":"https://a.example/p"}`))
			r.F.On("/token", tc.reply)
			p := r.Run(t, "mm")
			if p.Status != tc.status || p.Error != tc.code {
				t.Fatalf("%+v", p)
			}
		})
	}
}

func TestAMiniMaxTokenWithoutALifetimeGetsAnHour(t *testing.T) {
	r := login.NewLogin(t, "mm", minimaxBlock, oauth.Dialects{Device: map[string]oauth.DeviceDialect{"codebuddy": {Start: codebuddy.Start, Poll: codebuddy.Poll, Refresh: codebuddy.Refresh}, "minimax": {Start: minimax.Start, Poll: minimax.Poll}, "zcode": {Start: zcode.Start, Poll: zcode.Poll}, "kilocode": {Start: kilocode.Start, Poll: kilocode.Poll}}})
	r.F.On("/device", login.OK(`{"device_code":"DC","user_code":"UC","verification_uri":"https://a.example/p"}`))
	r.F.On("/token", login.OK(`{"access_token":"AT","refresh_token":"RT"}`))
	if p := r.Run(t, "mm"); p.Status != "done" {
		t.Fatalf("%+v", p)
	}
	if r.Accounts.Made(t).Fields["expiresIn"] != "3600" {
		t.Fatalf("%v", r.Accounts.Made(t).Fields)
	}
}

func TestAMiniMaxRenewalCarriesTheDataFieldsAndAClientsOwnCannotBeOverridden(t *testing.T) {
	r := login.NewLogin(t, "mm", minimaxBlock, oauth.Dialects{Device: map[string]oauth.DeviceDialect{"codebuddy": {Start: codebuddy.Start, Poll: codebuddy.Poll, Refresh: codebuddy.Refresh}, "minimax": {Start: minimax.Start, Poll: minimax.Poll}, "zcode": {Start: zcode.Start, Poll: zcode.Poll}, "kilocode": {Start: kilocode.Start, Poll: kilocode.Poll}}})
	f := flowOf(t, r, "mm", "client-mm")
	r.F.On("/token", login.OK(`{"access_token":"AT2","refresh_token":"RT2","expires_in":3600}`))
	if _, err := r.Service.Tokens.Refresh(context.Background(), f, "RT", nil); err != nil {
		t.Fatal(err)
	}
	v := formOf(t, r.F.Last("/token").Body)
	if v.Get("scope") != "agent.default" || v.Get("audience") != "agent-backend" || v.Get("grant_type") != "refresh_token" || v.Get("client_id") != "client-mm" || v.Get("refresh_token") != "RT" {
		t.Fatalf("%v", v)
	}
	bad := `{"id":"x","category":"oauth","transport":{"baseUrl":"https://x.example/v1/chat/completions"},"models":[],"oauth":` +
		strings.Replace(minimaxBlock, `"scope":"agent.default","audience"`, `"client_id":"x","audience"`, 1) + `}`
	bad = strings.Replace(bad, `"fields":{"scope"`, `"fields":{"client_id":"x","scope"`, 1)
	bad = strings.ReplaceAll(bad, "@", "https://x.example")
	if _, err := upstream.Load(fstest.MapFS{"data/x.json": {Data: []byte(bad)}}, "data"); err == nil {
		t.Fatal("a refresh field named like the client id was accepted")
	}
}

const zcodeBlock = `{"flow":"device","dialect":"zcode","noClient":true,"deviceUrl":"@/init","tokenUrl":"@/poll","scope":"-",` +
	`"endpoints":{"businessLogin":"@/login","api":"@"}}`

func zcodeRig(t *testing.T) *login.Login {
	t.Helper()
	r := login.NewLogin(t, "gl", zcodeBlock, oauth.Dialects{Device: map[string]oauth.DeviceDialect{"codebuddy": {Start: codebuddy.Start, Poll: codebuddy.Poll, Refresh: codebuddy.Refresh}, "minimax": {Start: minimax.Start, Poll: minimax.Poll}, "zcode": {Start: zcode.Start, Poll: zcode.Poll}, "kilocode": {Start: kilocode.Start, Poll: kilocode.Poll}}})
	r.F.On("/init", login.OK(`{"code":0,"data":{"flow_id":"F-1","authorize_url":"https://z.example/auth?f=1","poll_interval_sec":2,"expires_at":`+strconv.FormatInt(r.Now.Add(5*time.Minute).Unix(), 10)+`}}`))
	r.F.On("/poll/F-1", login.OK(`{"data":{"status":"ready","token":"JWT","zai":{"access_token":"ZAT"},"user":{"name":"Zed","user_id":"u1","email":"z@x.test"}}}`))
	r.F.On("/login", login.OK(`{"code":0,"data":{"access_token":"BIZ"}}`))
	r.F.On("/api/biz/customer/getCustomerInfo", login.OK(`{"code":0,"data":{"organizations":[`+
		`{"organizationId":"o-team","organizationName":"x","projects":[{"projectId":"p-team","projectType":"2"}]},`+
		`{"organizationId":"o-1","organizationName":"Other","projects":[{"projectId":"p-9","projectName":"x"}]},`+
		`{"organizationId":"o-2","organizationName":"默认机构","projects":[{"projectId":"p-a","projectName":"x"},{"projectId":"p-b","projectName":"默认项目"}]}]}}`))
	return r
}

const zcodeKeys = "/api/biz/v1/organization/o-2/projects/p-b/api_keys"

func TestAZCodeLoginEndsWithThePlanKeyAndStoresNothingElse(t *testing.T) {
	r := zcodeRig(t)
	r.F.On(zcodeKeys, login.OK(`{"code":0,"data":[{"name":"other","apiKey":"nope"},{"name":"zcode-api-key","apiKey":"AK"}]}`))
	r.F.On(zcodeKeys+"/copy/AK", login.OK(`{"code":0,"data":{"secretKey":"SK"}}`))
	s, err := r.Service.StartDevice(context.Background(), "gl", "bind")
	if err != nil || s.State == "" || s.Interval != 2 || s.VerificationURI != "https://z.example/auth?f=1" {
		t.Fatalf("%+v %v", s, err)
	}
	init := r.F.Last("/init")
	token := strings.TrimPrefix(init.Header.Get("Authorization"), "Bearer ")
	if len(token) != 64 || gjson.Get(init.Body, "provider").String() != "zai" {
		t.Fatalf("%+v", init)
	}
	r.Advance(3 * time.Second)
	p := r.Service.PollDevice(context.Background(), s.State, "bind")
	if p.Status != "done" {
		t.Fatalf("%+v", p)
	}
	if got := r.F.Last("/poll/F-1").Header.Get("Authorization"); got != "Bearer "+token {
		t.Fatalf("the poll token changed: %q", got)
	}
	if got := r.F.Last("/login").Body; gjson.Get(got, "token").String() != "ZAT" {
		t.Fatalf("%s", got)
	}
	in := r.Accounts.Made(t)
	if in.Fields["accessToken"] != `"AK.SK"` || in.Email != "z@x.test" {
		t.Fatalf("%+v", in)
	}
	if _, has := in.Fields["refreshToken"]; has {
		t.Fatal("a refresh token was stored")
	}
	if strings.Contains(in.PSD, "JWT") || strings.Contains(in.PSD, "BIZ") || strings.Contains(in.PSD, "ZAT") {
		t.Fatalf("a Z.ai token was kept: %s", in.PSD)
	}
	if psd := login.PSD(t, in); psd["username"] != "Zed" || psd["userId"] != "u1" {
		t.Fatalf("%v", psd)
	}
	if r.F.Count(zcodeKeys) != 1 {
		t.Fatal("the key list was read more than once")
	}
}

func TestAZCodeLoginMakesTheKeyWhenMissing(t *testing.T) {
	r := zcodeRig(t)
	r.F.On(zcodeKeys, login.OK(`{"code":0,"data":[]}`), login.OK(`{"code":0,"data":{"apiKey":"NEW"}}`))
	r.F.On(zcodeKeys+"/copy/NEW", login.OK(`{"code":0,"data":{"secretKey":"S"}}`))
	if p := r.Run(t, "gl"); p.Status != "done" {
		t.Fatalf("%+v", p)
	}
	if got := r.F.Last(zcodeKeys); got.Method != "POST" || gjson.Get(got.Body, "name").String() != "zcode-api-key" {
		t.Fatalf("%+v", got)
	}
}

func TestAZCodeLoginRefusesAnythingThatCouldChangeAPathAndStoresNothing(t *testing.T) {
	for name, mutate := range map[string]func(r *login.Login){
		"a project id with a slash": func(r *login.Login) {
			r.F.On("/api/biz/customer/getCustomerInfo", login.OK(`{"code":0,"data":{"organizations":[{"organizationId":"o","projects":[{"projectId":"../x","projectName":"default"}]}]}}`))
		},
		"an api key with a slash": func(r *login.Login) {
			r.F.On(zcodeKeys, login.OK(`{"code":0,"data":[{"name":"zcode-api-key","apiKey":"a/b"}]}`))
		},
		"a key with a dot": func(r *login.Login) {
			r.F.On(zcodeKeys, login.OK(`{"code":0,"data":[{"name":"zcode-api-key","apiKey":"a.b"}]}`))
		},
		"a business refusal":      func(r *login.Login) { r.F.On("/login", login.OK(`{"code":401,"msg":"SECRET"}`)) },
		"a business server error": func(r *login.Login) { r.F.On("/login", login.Reply{Status: 500, Body: "SECRET"}) },
		"no personal project": func(r *login.Login) {
			r.F.On("/api/biz/customer/getCustomerInfo", login.OK(`{"code":0,"data":{"organizations":[{"organizationId":"o","projects":[{"projectId":"p","projectType":"2"}]}]}}`))
		},
		"no secret": func(r *login.Login) {
			r.F.On(zcodeKeys, login.OK(`{"code":0,"data":[{"name":"zcode-api-key","apiKey":"AK"}]}`))
			r.F.On(zcodeKeys+"/copy/AK", login.OK(`{"code":0,"data":{}}`))
		},
		"a failed flow": func(r *login.Login) { r.F.On("/poll/F-1", login.OK(`{"data":{"status":"failed"}}`)) },
	} {
		t.Run(name, func(t *testing.T) {
			r := zcodeRig(t)
			r.F.On(zcodeKeys, login.OK(`{"code":0,"data":[{"name":"zcode-api-key","apiKey":"AK"}]}`))
			r.F.On(zcodeKeys+"/copy/AK", login.OK(`{"code":0,"data":{"secretKey":"SK"}}`))
			mutate(r)
			p := r.Run(t, "gl")
			if p.Status != "error" || strings.Contains(p.Error, "SECRET") || len(r.Accounts.All()) != 0 {
				t.Fatalf("%+v, %d stored", p, len(r.Accounts.All()))
			}
		})
	}
}

func TestAZCodePollWaitsOnPendingAndOnAnUnknownStatus(t *testing.T) {
	for _, body := range []string{`{"data":{"status":"pending"}}`, `{"data":{"status":"weird"}}`, `{"code":"0","data":{}}`} {
		r := zcodeRig(t)
		r.F.On("/poll/F-1", login.OK(body))
		if p := r.Run(t, "gl"); p.Status != "pending" {
			t.Errorf("%s: %+v", body, p)
		}
	}
}

func TestAZCodeStartIsCheckedBeforeItIsShown(t *testing.T) {
	for name, reply := range map[string]login.Reply{
		"a bad flow id":  login.OK(`{"data":{"flow_id":"a/b","authorize_url":"https://z.example/a"}}`),
		"an http page":   login.OK(`{"data":{"flow_id":"f","authorize_url":"http://z.example/a"}}`),
		"a failure":      login.OK(`{"code":500,"msg":"SECRET"}`),
		"no data":        login.OK(`{}`),
		"a server error": {Status: 500, Body: "SECRET"},
	} {
		r := zcodeRig(t)
		r.F.On("/init", reply)
		if _, err := r.Service.StartDevice(context.Background(), "gl", "bind"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestDeviceStateNeverLeavesTheServer(t *testing.T) {
	r := zcodeRig(t)
	r.F.On("/init", login.OK(`{"code":0,"data":{"flow_id":"F-1","authorize_url":"https://z.example/auth"}}`))
	s, _ := r.Service.StartDevice(context.Background(), "gl", "bind")
	token := strings.TrimPrefix(r.F.Last("/init").Header.Get("Authorization"), "Bearer ")
	raw, _ := json.Marshal(s)
	if strings.Contains(string(raw), token) {
		t.Fatal("the poll token reached the browser answer")
	}
}

func flowOf(t *testing.T, r *login.Login, id, client string) oauth.Flow {
	t.Helper()
	e, ok := r.Reg.ByID(id)
	if !ok {
		t.Fatal("no such provider")
	}
	f, err := oauth.NewFlow(e, client)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestADeviceStartSendsOnlyTheFieldsItHas(t *testing.T) {
	for name, tc := range map[string]struct {
		block string
		scope string
	}{
		"no scope": {`{"flow":"device","deviceUrl":"@/device","tokenUrl":"@/token","params":[{"name":"platform","value":"CLI"}]}`, ""},
		"a scope":  {`{"flow":"device","deviceUrl":"@/device","tokenUrl":"@/token","scope":"read:user"}`, "read:user"},
	} {
		t.Run(name, func(t *testing.T) {
			r := login.NewLogin(t, "dv", tc.block, oauth.Dialects{})
			r.F.On("/device", login.OK(`{"device_code":"DC","user_code":"UC","verification_uri":"https://a.example/v"}`))
			if _, err := r.Service.StartDevice(context.Background(), "dv", "bind"); err != nil {
				t.Fatal(err)
			}
			q, err := url.ParseQuery(r.F.Last("/device").Body)
			if err != nil {
				t.Fatal(err)
			}
			if q.Has("scope") != (tc.scope != "") || q.Get("scope") != tc.scope {
				t.Fatalf("scope %v, want %q", q["scope"], tc.scope)
			}
			if q.Get("client_id") != "client-dv" {
				t.Fatalf("client_id %q", q.Get("client_id"))
			}
		})
	}
}

// Meta refuses a scope on the device start, so the file that names none sends
// none.
func TestTheMuseDeviceStartCarriesNoScope(t *testing.T) {
	f, err := oauth.FlowFor(login.Registry(t), func(string) string { return "c" }, nil, "muse")
	if err != nil {
		t.Fatal(err)
	}
	if f.Flow != upstream.FlowDevice || f.Scope != "" {
		t.Fatalf("%s %q", f.Flow, f.Scope)
	}
}

func TestStartRefusesADeviceFlow(t *testing.T) {
	r := login.NewLogin(t, "muse", `{"flow":"device","deviceUrl":"@/device","tokenUrl":"@/token"}`, oauth.Dialects{})
	if _, err := r.Service.Start(oauth.StartRequest{Provider: "muse", Bind: "b"}); !errors.Is(err, oauth.ErrUnsupported) {
		t.Fatalf("%v", err)
	}
}

func TestAHugeLifetimeOrIntervalIsCappedNotWrapped(t *testing.T) {
	for _, n := range []int64{1 << 40, 1<<63 - 1, 9_223_372_036} {
		if got := oauth.ClampLife(n); got != 30*time.Minute {
			t.Errorf("life %d: %v", n, got)
		}
		if got := oauth.ClampInterval(n); got != time.Minute {
			t.Errorf("interval %d: %v", n, got)
		}
	}
}
