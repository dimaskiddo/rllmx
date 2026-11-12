package kilocode

import (
	"context"
	"strings"
	"testing"

	"rllmx/app/llm/oauth"
	"rllmx/tests/login"
)

// newLoginRig is the shared rig with this package's steps.
func newLoginRig(t *testing.T, id, block string) *login.Login {
	t.Helper()
	return login.NewLogin(t, id, block, oauth.Dialects{Device: map[string]oauth.DeviceDialect{"kilocode": {Start: Start, Poll: Poll}}, Hooks: map[string]oauth.Hook{"kilocode": Account}})
}

func TestPollingBySubmittedCodeNeedsNoClientIDForANoClientLogin(t *testing.T) {
	r := newLoginRig(t, "kc", `{"flow":"device","dialect":"kilocode","noClient":true,"deviceUrl":"@/device","tokenUrl":"@/codes","scope":"x"}`)
	r.Service.ClientID = func(string) string { return "" }
	r.F.On("/device", login.OK(`{"code":"ABC123","verificationUrl":"https://app.example/auth?c=ABC123","expiresIn":120}`))
	r.F.On("/codes/ABC123", login.OK(`{"status":"approved","token":"KILO-TOKEN","userEmail":"k@x.test"}`))
	p := r.Run(t, "kc")
	if p.Status != "done" {
		t.Fatalf("%+v", p)
	}
	in := r.Accounts.Made(t)
	if in.Email != "k@x.test" || in.Fields["accessToken"] != `"KILO-TOKEN"` {
		t.Fatalf("%+v", in)
	}
	if _, has := in.Fields["refreshToken"]; has {
		t.Fatal("a refresh token was invented")
	}
	start := r.F.Last("/device")
	if start.Method != "POST" || start.Body != "" || start.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("start: %+v", start)
	}
	if r.F.Last("/codes/ABC123").Method != "GET" {
		t.Fatal("the poll is not a GET")
	}
}

func TestTheKiloPollIsReadByItsHTTPStatus(t *testing.T) {
	for name, tc := range map[string]struct {
		reply  login.Reply
		status string
		code   string
	}{
		"waiting":                     {login.Reply{Status: 202, Body: `{}`}, "pending", ""},
		"approved without a token":    {login.OK(`{"status":"approved"}`), "pending", ""},
		"a token before approval":     {login.OK(`{"status":"pending","token":"T"}`), "pending", ""},
		"refused":                     {login.Reply{Status: 403, Body: `{"error":"SECRET"}`}, "error", oauth.CodeDenied},
		"expired":                     {login.Reply{Status: 410, Body: `{}`}, "error", oauth.CodeExpired},
		"a server error":              {login.Reply{Status: 500, Body: `<html>SECRET</html>`}, "error", "transport"},
		"another client error":        {login.Reply{Status: 404, Body: `x`}, "error", oauth.CodeRefused},
		"not json":                    {login.OK(`<html>`), "error", oauth.CodeRefused},
		"a token with a control byte": {login.OK("{\"status\":\"approved\",\"token\":\"a\\u0000b\"}"), "error", oauth.CodeRefused},
	} {
		t.Run(name, func(t *testing.T) {
			r := newLoginRig(t, "kc", `{"flow":"device","dialect":"kilocode","noClient":true,"deviceUrl":"@/device","tokenUrl":"@/codes","scope":"x"}`)
			r.F.On("/device", login.OK(`{"code":"ABC123","verificationUrl":"https://app.example/a"}`))
			r.F.On("/codes/ABC123", tc.reply)
			p := r.Run(t, "kc")
			if p.Status != tc.status || p.Error != tc.code {
				t.Fatalf("%+v", p)
			}
			if strings.Contains(p.Error, "SECRET") {
				t.Fatal("provider text in the answer")
			}
		})
	}
}

func TestTheKiloStartIsCheckedBeforeItIsShown(t *testing.T) {
	for name, tc := range map[string]struct {
		reply login.Reply
		class oauth.Class
	}{
		"too many pending":       {login.Reply{Status: 429, Body: `x`}, oauth.ClassRejected},
		"a server error":         {login.Reply{Status: 502, Body: `x`}, oauth.ClassTransport},
		"not an object":          {login.OK(`[]`), oauth.ClassMalformed},
		"a code with a slash":    {login.OK(`{"code":"a/b","verificationUrl":"https://app.example/a"}`), oauth.ClassMalformed},
		"a code with dots":       {login.OK(`{"code":"..","verificationUrl":"https://app.example/a"}`), oauth.ClassMalformed},
		"a code with a query":    {login.OK(`{"code":"a?b=1","verificationUrl":"https://app.example/a"}`), oauth.ClassMalformed},
		"no code":                {login.OK(`{"verificationUrl":"https://app.example/a"}`), oauth.ClassMalformed},
		"a page over plain http": {login.OK(`{"code":"abc","verificationUrl":"http://app.example/a"}`), oauth.ClassMalformed},
	} {
		t.Run(name, func(t *testing.T) {
			r := newLoginRig(t, "kc", `{"flow":"device","dialect":"kilocode","noClient":true,"deviceUrl":"@/device","tokenUrl":"@/codes","scope":"x"}`)
			r.F.On("/device", tc.reply)
			_, err := r.Service.StartDevice(context.Background(), "kc", "bind")
			if oauth.ClassOf(err) != tc.class {
				t.Fatalf("%v", err)
			}
		})
	}
}

// The poll puts the code in a request path, so it checks the code again even
// though the start already did.
func TestTheKiloPollRefusesACodeThatCouldChangeTheAddress(t *testing.T) {
	r := newLoginRig(t, "kc", `{"flow":"device","dialect":"kilocode","noClient":true,"deviceUrl":"@/device","tokenUrl":"@/codes","scope":"x"}`)
	r.F.On("/other", login.OK(`{"status":"approved","token":"STOLEN"}`))
	e, _ := r.Reg.ByID("kc")
	flow, err := oauth.NewFlow(e, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{"../other", "a/b", "a?x=1", "a#f", "", "a b", strings.Repeat("a", 65)} {
		_, _, err := Poll(context.Background(), r.Service.Tokens, flow, oauth.DeviceState{Code: code}, nil)
		if oauth.ClassOf(err) != oauth.ClassMalformed {
			t.Errorf("%q: %v", code, err)
		}
	}
	if n := r.F.Count("/other") + r.F.Count("/codes/a"); n != 0 {
		t.Fatalf("%d requests left for a refused code", n)
	}
}

func FuzzKiloCode(f *testing.F) {
	for _, s := range []string{"abc", "a/b", "..", "a?b", "", "a b", "é", strings.Repeat("a", 65), "a-b_c"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, code string) {
		if !kiloCode.MatchString(code) {
			return
		}
		if strings.ContainsAny(code, "/?#%\\ .:") || len(code) > 64 || code == "" {
			t.Fatalf("accepted %q", code)
		}
	})
}

func TestAKiloLoginReadsTheOrganisationAndIgnoresAnythingElse(t *testing.T) {
	for name, tc := range map[string]struct {
		profile login.Reply
		org     string
	}{
		"an organisation":        {login.OK(`{"organizations":[{"id":"org_1"},{"id":"org_2"}]}`), "org_1"},
		"none":                   {login.OK(`{"organizations":[]}`), ""},
		"a bad id":               {login.OK(`{"organizations":[{"id":"a/b"}]}`), ""},
		"an id that is a number": {login.OK(`{"organizations":[{"id":7}]}`), ""},
		"a server error":         {login.Reply{Status: 500, Body: `x`}, ""},
		"not json":               {login.OK(`<html>`), ""},
	} {
		t.Run(name, func(t *testing.T) {
			r := newLoginRig(t, "kc", `{"flow":"device","dialect":"kilocode","noClient":true,"deviceUrl":"@/device","tokenUrl":"@/codes","scope":"x","hook":"kilocode","endpoints":{"profile":"@/profile"}}`)
			r.F.On("/device", login.OK(`{"code":"ABC123","verificationUrl":"https://app.example/a"}`))
			r.F.On("/codes/ABC123", login.OK(`{"status":"approved","token":"KILO"}`))
			r.F.On("/profile", tc.profile)
			if p := r.Run(t, "kc"); p.Status != "done" {
				t.Fatalf("%+v", p)
			}
			in := r.Accounts.Made(t)
			if got, _ := login.PSD(t, in)["orgId"].(string); got != tc.org {
				t.Fatalf("orgId %q, want %q", got, tc.org)
			}
			if h := r.F.Last("/profile"); h.Header.Get("Authorization") != "Bearer KILO" {
				t.Fatalf("%v", h.Header)
			}
		})
	}
}
