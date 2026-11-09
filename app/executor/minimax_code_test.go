package executor

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/upstream"
	"rllmx/tests"
)

func mmRun(t *testing.T, replies ...tests.Reply) (*MinimaxCode, *tests.Upstream, func() (Response, string)) {
	t.Helper()
	up := tests.NewUpstream(t, replies...)
	m := &MinimaxCode{Default: &Default{Client: http.DefaultClient}, Now: func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.FixedZone("", 7*3600)) }}
	return m, up, func() (Response, string) {
		resp, err := m.Execute(context.Background(), Request{
			URL: up.URL, Body: []byte(`{}`), Key: "TOK",
			Auth: &upstream.Auth{Scheme: upstream.SchemeBearer, Header: "Authorization", Headers: map[string]string{"x-api-key": "sk-xxx"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return resp, string(raw)
	}
}

func TestAMiniMaxCallCarriesTheZoneAndASessionID(t *testing.T) {
	_, up, do := mmRun(t, tests.Reply{Body: `{}`}, tests.Reply{Body: `{}`})
	do()
	do()
	a, b := up.Calls()[0].Header, up.Calls()[1].Header
	if a.Get("X-Mavis-Timezone-Offset") != "25200" || a.Get("Authorization") != "Bearer TOK" || a.Get("X-Api-Key") != "sk-xxx" {
		t.Fatalf("%v", a)
	}
	if a.Get("X-Mavis-Session-Id") == "" || a.Get("X-Mavis-Session-Id") == b.Get("X-Mavis-Session-Id") {
		t.Fatalf("session ids %q %q", a.Get("X-Mavis-Session-Id"), b.Get("X-Mavis-Session-Id"))
	}
}

func TestAMiniMaxCallWithoutACredentialSendsNothing(t *testing.T) {
	up := tests.NewUpstream(t)
	m := &MinimaxCode{Default: &Default{Client: http.DefaultClient}}
	if _, err := m.Execute(context.Background(), Request{URL: up.URL}); err != ErrNoCredential || len(up.Calls()) != 0 {
		t.Fatalf("%v", err)
	}
}

func TestASpentBalanceBecomesA429AndAnAuthRefusalStays(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
		want   int
		msg    string
	}{
		"402 with a balance":       {402, `{"error":{"message":"Insufficient balance"}}`, 429, "usage limit reached: Insufficient balance"},
		"403 with credits":         {403, `{"message":"no credits left"}`, 429, "usage limit reached: no credits left"},
		"403 in Chinese":           {403, `{"msg":"余额不足"}`, 429, "usage limit reached: 余额不足"},
		"base_resp message":        {402, `{"base_resp":{"status_msg":"quota exhausted"}}`, 429, "usage limit reached: quota exhausted"},
		"plain text":               {402, `quota gone`, 429, "usage limit reached: quota gone"},
		"403 that is an auth fail": {403, `{"message":"invalid token"}`, 403, ""},
		"402 with nothing":         {402, `{}`, 402, ""},
		"429 passes":               {429, `{"message":"quota"}`, 429, ""},
		"500 passes":               {500, `{"message":"quota"}`, 500, ""},
	} {
		_, _, do := mmRun(t, tests.Reply{Status: tc.status, Body: tc.body})
		resp, body := do()
		if resp.Status != tc.want {
			t.Errorf("%s: status %d", name, resp.Status)
			continue
		}
		if tc.msg == "" {
			if body != tc.body {
				t.Errorf("%s: body rewritten: %s", name, body)
			}
			continue
		}
		if gjson.Get(body, "error.message").String() != tc.msg || gjson.Get(body, "error.type").String() != "rate_limit_error" || gjson.Get(body, "type").String() != "error" {
			t.Errorf("%s: %s", name, body)
		}
		if !strings.Contains(resp.Header.Get("Content-Type"), "json") {
			t.Errorf("%s: content type %q", name, resp.Header.Get("Content-Type"))
		}
	}
}

func TestAGoodMiniMaxAnswerIsNotTouched(t *testing.T) {
	_, _, do := mmRun(t, tests.Reply{Status: 200, Body: `{"insufficient":"balance"}`})
	if resp, body := do(); resp.Status != 200 || body != `{"insufficient":"balance"}` {
		t.Fatalf("%d %s", resp.Status, body)
	}
}
