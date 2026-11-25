package controllers

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// webStub answers the web probes' fixed addresses.
type webStub map[string]struct {
	status int
	body   string
	err    error
}

func (s webStub) RoundTrip(r *http.Request) (*http.Response, error) {
	answer, ok := s[r.URL.Host+r.URL.Path]
	if !ok {
		return nil, errors.New("no answer for " + r.URL.Host + r.URL.Path)
	}
	if answer.err != nil {
		return nil, answer.err
	}
	status := answer.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(answer.body))}, nil
}

func webChecker(s webStub) *Checker {
	return &Checker{client: &http.Client{Transport: s}, cap: 1 << 20}
}

func TestAWebSessionProbeJudgesTheSignedInAnswer(t *testing.T) {
	c := webChecker(webStub{
		"chatgpt.com/api/auth/session": {body: `{"user":{"email":"a@b.c"}}`},
		"gemini.google.com/app":        {body: `<script>"SNlM0e":"x"</script>`},
		"muse.ai/api/session":          {body: `{}`},
		"grok.com/api/auth/session":    {body: `{"status":"authenticated"}`},
	})
	cookie := "__Secure-next-auth.session-token=v"
	if res := c.CheckWeb(context.Background(), "chatgpt-web", cookie); !res.Valid() {
		t.Fatalf("chatgpt: %+v", res)
	}
	if res := c.CheckWeb(context.Background(), "gemini-web", "__Secure-1PSID=v"); !res.Valid() {
		t.Fatalf("gemini: %+v", res)
	}
	if res := c.CheckWeb(context.Background(), "muse-web", "hatch_sess=v"); !res.Valid() {
		t.Fatalf("muse: %+v", res)
	}
	if res := c.CheckWeb(context.Background(), "grok-web", "sso=v"); !res.Valid() {
		t.Fatalf("grok: %+v", res)
	}
	for _, bad := range []struct{ provider, cookie string }{
		{"chatgpt-web", "other=1"},
		{"chatgpt-web", "__Secure-next-auth.session-token=v"},
	} {
		res := c.CheckWeb(context.Background(), bad.provider, bad.cookie)
		if res.Valid() && bad.cookie == "other=1" || res.Warning && bad.cookie == "other=1" {
			t.Errorf("%s %q: %+v", bad.provider, bad.cookie, res)
		}
	}
}

func TestAWebSessionProbeKeepsAnAccountOnANetworkFault(t *testing.T) {
	c := webChecker(webStub{"chatgpt.com/api/auth/session": {err: errors.New("dial")}})
	res := c.CheckWeb(context.Background(), "chatgpt-web", "__Secure-next-auth.session-token=v")
	if !res.Valid() || !res.Warning {
		t.Fatalf("%+v", res)
	}
}

func TestAWebSessionProbeRefusesASignedOutPage(t *testing.T) {
	c := webChecker(webStub{
		"gemini.google.com/app":     {body: `<html>sign in</html>`},
		"grok.com/api/auth/session": {body: `{"status":"signed-out"}`},
	})
	if res := c.CheckWeb(context.Background(), "gemini-web", "__Secure-1PSID=v"); res.Valid() {
		t.Fatalf("gemini: %+v", res)
	}
	if res := c.CheckWeb(context.Background(), "grok-web", "sso=v"); res.Valid() {
		t.Fatalf("grok: %+v", res)
	}
}

func TestTheDeepSeekWebTokenIsJudgedByTheAccountAnswer(t *testing.T) {
	c := webChecker(webStub{
		"chat.deepseek.com/api/v0/users/current": {body: `{"code":0,"data":{"biz_data":{"token":"t"}}}`},
	})
	if res := c.CheckDeepSeekWeb(context.Background(), `{"value":"tok"}`); !res.Valid() {
		t.Fatalf("%+v", res)
	}
	c = webChecker(webStub{"chat.deepseek.com/api/v0/users/current": {body: `{"code":40003}`}})
	if res := c.CheckDeepSeekWeb(context.Background(), "tok"); res.Valid() {
		t.Fatalf("%+v", res)
	}
}
