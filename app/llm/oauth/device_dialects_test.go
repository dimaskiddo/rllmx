package oauth

import (
	"context"
	"encoding/json"
	"net/url"
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
	r := newLoginRig(t, "cb", codebuddyBlock)
	r.f.on("/state", ok(`{"code":0,"data":{"state":"ST ATE&1","authUrl":"https://auth.example/login?s=1"}}`))
	r.f.on("/token", ok(`{"code":11217,"msg":"RETRY"}`), ok(`{"code":0,"data":{"accessToken":"AT","refreshToken":"RT","expiresIn":7200}}`))
	s, err := r.svc.StartDevice(context.Background(), "cb", "bind")
	if err != nil || s.VerificationURI != "https://auth.example/login?s=1" || s.UserCode != "" || s.Interval != 5 {
		t.Fatalf("%+v %v", s, err)
	}
	start := r.f.last("/state")
	if start.Query != "platform=CLI" || start.Body != "{}" || start.Header.Get("X-No-Authorization") != "true" || start.Header.Get("X-Domain") != "d.example" {
		t.Fatalf("start %+v", start)
	}
	r.advance(6 * time.Second)
	if p := r.svc.PollDevice(context.Background(), s.State, "bind"); p.Status != "pending" {
		t.Fatalf("%+v", p)
	}
	r.advance(6 * time.Second)
	p := r.svc.PollDevice(context.Background(), s.State, "bind")
	if p.Status != "done" {
		t.Fatalf("%+v", p)
	}
	poll := r.f.last("/token")
	if poll.Method != "GET" || poll.Query != "state=ST+ATE%261" || poll.Header.Get("X-No-User-Id") != "true" {
		t.Fatalf("poll %+v", poll)
	}
	in := r.made(t)
	if in.Fields["accessToken"] != `"AT"` || in.Fields["refreshToken"] != `"RT"` || in.Fields["expiresIn"] != "7200" {
		t.Fatalf("%+v", in.Fields)
	}
}

func TestACodeBuddyAnswerIsCheckedAndNeverShown(t *testing.T) {
	for name, tc := range map[string]struct {
		reply fakeReply
		class Class
	}{
		"a refused code":     {ok(`{"code":500,"msg":"SECRET"}`), ClassMalformed},
		"no state":           {ok(`{"code":0,"data":{"authUrl":"https://a.example/x"}}`), ClassMalformed},
		"a plain http page":  {ok(`{"code":0,"data":{"state":"s","authUrl":"http://a.example/x"}}`), ClassMalformed},
		"a server error":     {fakeReply{Status: 500, Body: "SECRET"}, ClassTransport},
		"not an object":      {ok(`[]`), ClassMalformed},
		"a state with a NUL": {ok("{\"code\":0,\"data\":{\"state\":\"a\\u0000\",\"authUrl\":\"https://a.example/x\"}}"), ClassMalformed},
	} {
		t.Run(name, func(t *testing.T) {
			r := newLoginRig(t, "cb", codebuddyBlock)
			r.f.on("/state", tc.reply)
			_, err := r.svc.StartDevice(context.Background(), "cb", "bind")
			if ClassOf(err) != tc.class {
				t.Fatalf("%v", err)
			}
		})
	}
}

func TestACodeBuddyPollEndsOnAnythingButTheBusyCode(t *testing.T) {
	for name, tc := range map[string]struct {
		reply fakeReply
		want  string
	}{
		"another code":        {ok(`{"code":1,"msg":"SECRET"}`), CodeRefused},
		"a server error":      {fakeReply{Status: 503, Body: "SECRET"}, "transport"},
		"zero without tokens": {ok(`{"code":0,"data":{}}`), CodeRefused},
		"no code":             {ok(`{"data":{"accessToken":"AT"}}`), CodeRefused},
	} {
		t.Run(name, func(t *testing.T) {
			r := newLoginRig(t, "cb", codebuddyBlock)
			r.f.on("/state", ok(`{"code":0,"data":{"state":"s","authUrl":"https://a.example/x"}}`))
			r.f.on("/token", tc.reply)
			p := r.run(t, "cb")
			if p.Status != "error" || p.Error != tc.want || strings.Contains(p.Error, "SECRET") {
				t.Fatalf("%+v", p)
			}
		})
	}
}

func TestACodeBuddyRenewalSendsTheRefreshTokenInAHeader(t *testing.T) {
	r := newLoginRig(t, "cb", codebuddyBlock)
	f := flowOf(t, r, "cb", "")
	r.f.on("/refresh", ok(`{"code":0,"data":{"accessToken":"AT2","expiresIn":100}}`))
	tok, err := r.svc.Tokens.Refresh(context.Background(), f, "RT", callHeaders(f, ""))
	if err != nil || tok.Access != "AT2" || tok.Refresh != "RT" || tok.ExpiresIn != 100 {
		t.Fatalf("%+v %v", tok, err)
	}
	h := r.f.last("/refresh")
	if h.Header.Get("X-Refresh-Token") != "RT" || h.Header.Get("X-Auth-Refresh-Source") != "plugin" || h.Body != "{}" || h.Header.Get("X-Domain") != "d.example" {
		t.Fatalf("%+v", h)
	}
	for name, tc := range map[string]struct {
		reply fakeReply
		class Class
	}{
		"a 401 kills the sign-in":         {fakeReply{Status: 401, Body: "x"}, ClassInvalidGrant},
		"a refusal in the body is not":    {ok(`{"code":9,"msg":"invalid_grant"}`), ClassRejected},
		"a server error":                  {fakeReply{Status: 500, Body: "x"}, ClassTransport},
		"a code zero with no token":       {ok(`{"code":0,"data":{}}`), ClassMalformed},
		"an answer that is not an object": {ok(`[]`), ClassMalformed},
	} {
		r.f.on("/refresh", tc.reply)
		if _, err := r.svc.Tokens.Refresh(context.Background(), f, "RT", nil); ClassOf(err) != tc.class {
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
	r := newLoginRig(t, "mm", minimaxBlock)
	r.f.on("/device", ok(`{"device_code":"DC","user_code":"UC","verification_uri":"https://a.example/p","verification_uri_complete":"https://a.example/p?c=UC","expires_in":300,"interval":4}`))
	r.f.on("/token", ok(`{"access_token":"AT","refresh_token":"RT","expires_in":3600}`))
	s, err := r.svc.StartDevice(context.Background(), "mm", "bind")
	if err != nil || s.VerificationURI != "https://a.example/p?c=UC" || s.Interval != 4 || s.ExpiresIn != 300 || s.UserCode != "UC" {
		t.Fatalf("%+v %v", s, err)
	}
	start := formOf(t, r.f.last("/device").Body)
	if start.Get("client_id") != "client-mm" || start.Get("scope") != "agent.default" || start.Get("audience") != "agent-backend" || start.Get("code_challenge_method") != "S256" {
		t.Fatalf("%v", start)
	}
	r.advance(5 * time.Second)
	if p := r.svc.PollDevice(context.Background(), s.State, "bind"); p.Status != "done" {
		t.Fatalf("%+v", p)
	}
	poll := formOf(t, r.f.last("/token").Body)
	if poll.Get("device_code") != "DC" || poll.Has("user_code") || poll.Get("grant_type") != minimaxGrant || oidc.Challenge(poll.Get("code_verifier")) != start.Get("code_challenge") {
		t.Fatalf("%v", poll)
	}
}

func TestAMiniMaxLoginWithoutADeviceCodePollsWithTheUserCode(t *testing.T) {
	r := newLoginRig(t, "mm", minimaxBlock)
	r.f.on("/device", ok(`{"user_code":"UC","verification_uri":"https://a.example/p","expired_in":`+strconv.FormatInt(r.now.Add(10*time.Minute).UnixMilli(), 10)+`,"interval":5000}`))
	r.f.on("/token", ok(`{"status":"pending"}`))
	s, err := r.svc.StartDevice(context.Background(), "mm", "bind")
	if err != nil || s.Interval != 5 || s.ExpiresIn < 590 || s.ExpiresIn > 600 {
		t.Fatalf("%+v %v", s, err)
	}
	r.advance(6 * time.Second)
	if p := r.svc.PollDevice(context.Background(), s.State, "bind"); p.Status != "pending" {
		t.Fatalf("%+v", p)
	}
	poll := formOf(t, r.f.last("/token").Body)
	if poll.Get("user_code") != "UC" || poll.Has("device_code") {
		t.Fatalf("%v", poll)
	}
}

func TestAMiniMaxPollReadsItsStatusWordsAndErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		reply  fakeReply
		status string
		code   string
	}{
		"pending":       {ok(`{"status":"pending"}`), "pending", ""},
		"slow down":     {ok(`{"status":"slow_down"}`), "pending", ""},
		"denied":        {ok(`{"status":"denied"}`), "error", CodeDenied},
		"expired":       {ok(`{"status":"expired"}`), "error", CodeExpired},
		"standard form": {fakeReply{Status: 400, Body: `{"error":"authorization_pending"}`}, "pending", ""},
		"standard deny": {fakeReply{Status: 400, Body: `{"error":"access_denied"}`}, "error", CodeDenied},
		"an unknown":    {ok(`{"status":"weird","error_description":"SECRET"}`), "error", CodeRefused},
		"no token":      {ok(`{"foo":1}`), "error", CodeRefused},
		"server error":  {fakeReply{Status: 502, Body: "SECRET"}, "error", "transport"},
	} {
		t.Run(name, func(t *testing.T) {
			r := newLoginRig(t, "mm", minimaxBlock)
			r.f.on("/device", ok(`{"device_code":"DC","user_code":"UC","verification_uri":"https://a.example/p"}`))
			r.f.on("/token", tc.reply)
			p := r.run(t, "mm")
			if p.Status != tc.status || p.Error != tc.code {
				t.Fatalf("%+v", p)
			}
		})
	}
}

func TestAMiniMaxTokenWithoutALifetimeGetsAnHour(t *testing.T) {
	r := newLoginRig(t, "mm", minimaxBlock)
	r.f.on("/device", ok(`{"device_code":"DC","user_code":"UC","verification_uri":"https://a.example/p"}`))
	r.f.on("/token", ok(`{"access_token":"AT","refresh_token":"RT"}`))
	if p := r.run(t, "mm"); p.Status != "done" {
		t.Fatalf("%+v", p)
	}
	if r.made(t).Fields["expiresIn"] != "3600" {
		t.Fatalf("%v", r.made(t).Fields)
	}
}

func TestAMiniMaxRenewalCarriesTheDataFieldsAndAClientsOwnCannotBeOverridden(t *testing.T) {
	r := newLoginRig(t, "mm", minimaxBlock)
	f := flowOf(t, r, "mm", "client-mm")
	r.f.on("/token", ok(`{"access_token":"AT2","refresh_token":"RT2","expires_in":3600}`))
	if _, err := r.svc.Tokens.Refresh(context.Background(), f, "RT", nil); err != nil {
		t.Fatal(err)
	}
	v := formOf(t, r.f.last("/token").Body)
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

func zcodeRig(t *testing.T) *loginRig {
	t.Helper()
	r := newLoginRig(t, "gl", zcodeBlock)
	r.f.on("/init", ok(`{"code":0,"data":{"flow_id":"F-1","authorize_url":"https://z.example/auth?f=1","poll_interval_sec":2,"expires_at":`+strconv.FormatInt(r.now.Add(5*time.Minute).Unix(), 10)+`}}`))
	r.f.on("/poll/F-1", ok(`{"data":{"status":"ready","token":"JWT","zai":{"access_token":"ZAT"},"user":{"name":"Zed","user_id":"u1","email":"z@x.test"}}}`))
	r.f.on("/login", ok(`{"code":0,"data":{"access_token":"BIZ"}}`))
	r.f.on("/api/biz/customer/getCustomerInfo", ok(`{"code":0,"data":{"organizations":[`+
		`{"organizationId":"o-team","organizationName":"x","projects":[{"projectId":"p-team","projectType":"2"}]},`+
		`{"organizationId":"o-1","organizationName":"Other","projects":[{"projectId":"p-9","projectName":"x"}]},`+
		`{"organizationId":"o-2","organizationName":"默认机构","projects":[{"projectId":"p-a","projectName":"x"},{"projectId":"p-b","projectName":"默认项目"}]}]}}`))
	return r
}

const zcodeKeys = "/api/biz/v1/organization/o-2/projects/p-b/api_keys"

func TestAZCodeLoginEndsWithThePlanKeyAndStoresNothingElse(t *testing.T) {
	r := zcodeRig(t)
	r.f.on(zcodeKeys, ok(`{"code":0,"data":[{"name":"other","apiKey":"nope"},{"name":"zcode-api-key","apiKey":"AK"}]}`))
	r.f.on(zcodeKeys+"/copy/AK", ok(`{"code":0,"data":{"secretKey":"SK"}}`))
	s, err := r.svc.StartDevice(context.Background(), "gl", "bind")
	if err != nil || s.State == "" || s.Interval != 2 || s.VerificationURI != "https://z.example/auth?f=1" {
		t.Fatalf("%+v %v", s, err)
	}
	init := r.f.last("/init")
	token := strings.TrimPrefix(init.Header.Get("Authorization"), "Bearer ")
	if len(token) != 64 || gjson.Get(init.Body, "provider").String() != "zai" {
		t.Fatalf("%+v", init)
	}
	r.advance(3 * time.Second)
	p := r.svc.PollDevice(context.Background(), s.State, "bind")
	if p.Status != "done" {
		t.Fatalf("%+v", p)
	}
	if got := r.f.last("/poll/F-1").Header.Get("Authorization"); got != "Bearer "+token {
		t.Fatalf("the poll token changed: %q", got)
	}
	if got := r.f.last("/login").Body; gjson.Get(got, "token").String() != "ZAT" {
		t.Fatalf("%s", got)
	}
	in := r.made(t)
	if in.Fields["accessToken"] != `"AK.SK"` || in.Email != "z@x.test" {
		t.Fatalf("%+v", in)
	}
	if _, has := in.Fields["refreshToken"]; has {
		t.Fatal("a refresh token was stored")
	}
	if strings.Contains(in.PSD, "JWT") || strings.Contains(in.PSD, "BIZ") || strings.Contains(in.PSD, "ZAT") {
		t.Fatalf("a Z.ai token was kept: %s", in.PSD)
	}
	if psd := psdOf(t, in); psd["username"] != "Zed" || psd["userId"] != "u1" {
		t.Fatalf("%v", psd)
	}
	if r.f.count(zcodeKeys) != 1 {
		t.Fatal("the key list was read more than once")
	}
}

func TestAZCodeLoginMakesTheKeyWhenMissing(t *testing.T) {
	r := zcodeRig(t)
	r.f.on(zcodeKeys, ok(`{"code":0,"data":[]}`), ok(`{"code":0,"data":{"apiKey":"NEW"}}`))
	r.f.on(zcodeKeys+"/copy/NEW", ok(`{"code":0,"data":{"secretKey":"S"}}`))
	if p := r.run(t, "gl"); p.Status != "done" {
		t.Fatalf("%+v", p)
	}
	if got := r.f.last(zcodeKeys); got.Method != "POST" || gjson.Get(got.Body, "name").String() != "zcode-api-key" {
		t.Fatalf("%+v", got)
	}
}

func TestAZCodeLoginRefusesAnythingThatCouldChangeAPathAndStoresNothing(t *testing.T) {
	for name, mutate := range map[string]func(r *loginRig){
		"a project id with a slash": func(r *loginRig) {
			r.f.on("/api/biz/customer/getCustomerInfo", ok(`{"code":0,"data":{"organizations":[{"organizationId":"o","projects":[{"projectId":"../x","projectName":"default"}]}]}}`))
		},
		"an api key with a slash": func(r *loginRig) {
			r.f.on(zcodeKeys, ok(`{"code":0,"data":[{"name":"zcode-api-key","apiKey":"a/b"}]}`))
		},
		"a key with a dot": func(r *loginRig) {
			r.f.on(zcodeKeys, ok(`{"code":0,"data":[{"name":"zcode-api-key","apiKey":"a.b"}]}`))
		},
		"a business refusal":      func(r *loginRig) { r.f.on("/login", ok(`{"code":401,"msg":"SECRET"}`)) },
		"a business server error": func(r *loginRig) { r.f.on("/login", fakeReply{Status: 500, Body: "SECRET"}) },
		"no personal project": func(r *loginRig) {
			r.f.on("/api/biz/customer/getCustomerInfo", ok(`{"code":0,"data":{"organizations":[{"organizationId":"o","projects":[{"projectId":"p","projectType":"2"}]}]}}`))
		},
		"no secret": func(r *loginRig) {
			r.f.on(zcodeKeys, ok(`{"code":0,"data":[{"name":"zcode-api-key","apiKey":"AK"}]}`))
			r.f.on(zcodeKeys+"/copy/AK", ok(`{"code":0,"data":{}}`))
		},
		"a failed flow": func(r *loginRig) { r.f.on("/poll/F-1", ok(`{"data":{"status":"failed"}}`)) },
	} {
		t.Run(name, func(t *testing.T) {
			r := zcodeRig(t)
			r.f.on(zcodeKeys, ok(`{"code":0,"data":[{"name":"zcode-api-key","apiKey":"AK"}]}`))
			r.f.on(zcodeKeys+"/copy/AK", ok(`{"code":0,"data":{"secretKey":"SK"}}`))
			mutate(r)
			p := r.run(t, "gl")
			if p.Status != "error" || strings.Contains(p.Error, "SECRET") || len(r.accounts.made) != 0 {
				t.Fatalf("%+v, %d stored", p, len(r.accounts.made))
			}
		})
	}
}

func TestAZCodePollWaitsOnPendingAndOnAnUnknownStatus(t *testing.T) {
	for _, body := range []string{`{"data":{"status":"pending"}}`, `{"data":{"status":"weird"}}`, `{"code":"0","data":{}}`} {
		r := zcodeRig(t)
		r.f.on("/poll/F-1", ok(body))
		if p := r.run(t, "gl"); p.Status != "pending" {
			t.Errorf("%s: %+v", body, p)
		}
	}
}

func TestAZCodeStartIsCheckedBeforeItIsShown(t *testing.T) {
	for name, reply := range map[string]fakeReply{
		"a bad flow id":  ok(`{"data":{"flow_id":"a/b","authorize_url":"https://z.example/a"}}`),
		"an http page":   ok(`{"data":{"flow_id":"f","authorize_url":"http://z.example/a"}}`),
		"a failure":      ok(`{"code":500,"msg":"SECRET"}`),
		"no data":        ok(`{}`),
		"a server error": {Status: 500, Body: "SECRET"},
	} {
		r := zcodeRig(t)
		r.f.on("/init", reply)
		if _, err := r.svc.StartDevice(context.Background(), "gl", "bind"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestDeviceStateNeverLeavesTheServer(t *testing.T) {
	r := zcodeRig(t)
	r.f.on("/init", ok(`{"code":0,"data":{"flow_id":"F-1","authorize_url":"https://z.example/auth"}}`))
	s, _ := r.svc.StartDevice(context.Background(), "gl", "bind")
	token := strings.TrimPrefix(r.f.last("/init").Header.Get("Authorization"), "Bearer ")
	raw, _ := json.Marshal(s)
	if strings.Contains(string(raw), token) {
		t.Fatal("the poll token reached the browser answer")
	}
}

func FuzzZCodeLocation(f *testing.F) {
	for _, s := range []string{`{}`, `{"organizations":[]}`, `{"organizations":[{"organizationId":"o","projects":[{"projectId":"p"}]}]}`, `[`, `{"organizations":[{"organizationId":"../","projects":[{"projectId":"p"}]}]}`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		org, project, ok := zcodeLocation(gjson.ParseBytes(raw))
		if ok && (!zcodeID.MatchString(org) || !zcodeID.MatchString(project)) {
			t.Fatalf("accepted %q %q", org, project)
		}
	})
}

func FuzzMiniMaxGrant(f *testing.F) {
	for _, s := range []string{`{}`, `{"user_code":"u","verification_uri":"https://a.example/x","expired_in":1}`, `{"device_code":"d","user_code":"u","verification_uri":"https://a.example/x","interval":-5,"expires_in":99999999999}`, `[`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		body := gjson.ParseBytes(raw)
		if d := minimaxDeadline(body, time.Unix(1_700_000_000, 0)); d <= 0 || d > maxLife {
			t.Fatalf("life %v", d)
		}
		for _, by := range []bool{true, false} {
			if i := minimaxInterval(body, by); i < minInterval || i > maxInterval {
				t.Fatalf("interval %v", i)
			}
		}
	})
}

func flowOf(t *testing.T, r *loginRig, id, client string) Flow {
	t.Helper()
	e, ok := r.reg.ByID(id)
	if !ok {
		t.Fatal("no such provider")
	}
	f, err := NewFlow(e, client)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestAHugeLifetimeOrIntervalIsCappedNotWrapped(t *testing.T) {
	for _, n := range []int64{1 << 40, 1<<63 - 1, 9_223_372_036} {
		if got := clampLife(n); got != maxLife {
			t.Errorf("life %d: %v", n, got)
		}
		if got := clampInterval(n); got != maxInterval {
			t.Errorf("interval %d: %v", n, got)
		}
	}
}
