package google

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/executor"
	"rllmx/app/llm/upstream"
	"rllmx/tests"
)

const envelope = `{"project":"","model":"gemini-3-pro-preview","userAgent":"x","requestId":"","request":{"contents":[{"role":"user","parts":[{"text":"hi"}]},{"role":"model","parts":[{"text":"yo"}]},{"role":"user","parts":[{"text":"again"}]}]}}`

func ccRun(t *testing.T, ag bool, replies ...tests.Reply) (*CloudCode, *tests.Upstream, executor.Request) {
	t.Helper()
	up := tests.NewUpstream(t, replies...)
	slept := []time.Duration{}
	d := &executor.Default{Client: http.DefaultClient, Sleep: func(dur time.Duration) <-chan time.Time {
		slept = append(slept, dur)
		ch := make(chan time.Time, 1)
		ch <- time.Now()
		return ch
	}}
	c := &CloudCode{Default: d, Antigravity: ag, Now: func() time.Time { return time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC) }}
	req := executor.Request{
		URL: up.URL + "/v1internal", Body: []byte(envelope), Key: "TOK", Model: "gemini-3-pro-preview", Session: "sess-1",
		Account: tests.Account(map[string]string{"projectId": "my-proj-123"}),
		Auth:    &upstream.Auth{Scheme: upstream.SchemeBearer, Header: "Authorization"},
		Entry:   upstream.Entry{Transport: upstream.Transport{Headers: map[string]string{cliAgentHeader: "GeminiCLI/0.34.0/{model} (router; router; terminal)"}}},
	}
	return c, up, req
}

func ccDo(t *testing.T, c *CloudCode, req executor.Request) (executor.Response, string) {
	t.Helper()
	resp, err := c.Execute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp, string(raw)
}

func TestGeminiCLIFillsTheProjectAndIDsAndAddressesTheAction(t *testing.T) {
	c, up, req := ccRun(t, false, tests.Reply{Body: `{}`}, tests.Reply{Body: `{}`})
	req.Stream = true
	ccDo(t, c, req)
	req.Stream = false
	ccDo(t, c, req)
	a, b := up.Calls()[0], up.Calls()[1]
	if a.Path != "/v1internal:streamGenerateContent" || a.Query != "alt=sse" || b.Path != "/v1internal:generateContent" {
		t.Fatalf("%s?%s %s", a.Path, a.Query, b.Path)
	}
	g := gjson.ParseBytes(a.Body)
	if g.Get("project").String() != "my-proj-123" || !strings.HasPrefix(g.Get("requestId").String(), "agent-") || !strings.HasPrefix(g.Get("request.sessionId").String(), "-") {
		t.Fatalf("%s", a.Body)
	}
	if a.Header.Get("Authorization") != "Bearer TOK" || a.Header.Get("User-Agent") != "GeminiCLI/0.34.0/gemini-3-pro-preview (router; router; terminal)" || a.Header.Get("Accept") != "text/event-stream" {
		t.Fatalf("%v", a.Header)
	}
	if gjson.GetBytes(b.Body, "requestId").String() == g.Get("requestId").String() {
		t.Fatal("two requests share one id")
	}
}

func TestAntigravityRequestIDKeepsTheConversationAndNumbersTheStep(t *testing.T) {
	c, up, req := ccRun(t, true, tests.Reply{Body: `{}`}, tests.Reply{Body: `{}`})
	ccDo(t, c, req)
	req.Session = "sess-2"
	ccDo(t, c, req)
	first, second := gjson.GetBytes(up.Calls()[0].Body, "requestId").String(), gjson.GetBytes(up.Calls()[1].Body, "requestId").String()
	pa, pb := strings.Split(first, "/"), strings.Split(second, "/")
	if len(pa) != 5 || pa[0] != "agent" || pa[4] != "5" {
		t.Fatalf("%s", first)
	}
	if pa[1] == pb[1] || pa[3] == pb[3] {
		t.Fatalf("two sessions share ids: %s %s", first, second)
	}
	a, b := seededUUID("x"), seededUUID("x")
	if a != b || a == seededUUID("y") {
		t.Fatal("seeded ids are not stable")
	}
	if gjson.GetBytes(up.Calls()[0].Body, "requestType").Exists() {
		t.Fatalf("chat sent a requestType: %s", up.Calls()[0].Body)
	}
	if h := up.Calls()[0].Header.Get("User-Agent"); strings.HasPrefix(h, "GeminiCLI") {
		t.Fatalf("antigravity sent the Gemini CLI agent: %s", h)
	}
}

func TestAnImageModelOnAntigravityIsNeverStreamed(t *testing.T) {
	c, up, req := ccRun(t, true, tests.Reply{Body: `{}`})
	req.Model, req.Stream = "gemini-3.1-flash-image", true
	ccDo(t, c, req)
	if call := up.Calls()[0]; !strings.HasSuffix(call.Path, ":generateContent") || call.Query != "" {
		t.Fatalf("%s?%s", call.Path, call.Query)
	}
}

func TestAnAccountWithoutAValidProjectSendsNothing(t *testing.T) {
	for name, project := range map[string]string{"missing": "", "path": "../x", "upper": "My-Proj", "space": "my proj"} {
		c, up, req := ccRun(t, true, tests.Reply{Body: `{}`})
		req.Account = tests.Account(map[string]string{"projectId": project})
		if _, err := c.Execute(context.Background(), req); err != executor.ErrBadAccount || len(up.Calls()) != 0 {
			t.Errorf("%s: %v, %d calls", name, err, len(up.Calls()))
		}
	}
	c, _, req := ccRun(t, false)
	req.Key = ""
	if _, err := c.Execute(context.Background(), req); err != executor.ErrNoCredential {
		t.Fatalf("%v", err)
	}
}

func TestGoogleResetHintsBecomeTheResetTheLockReads(t *testing.T) {
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	retryInfo := `{"error":{"message":"slow","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"30s"}]}}`
	quota := `{"error":{"message":"out","details":[{"metadata":{"quotaResetTimeStamp":"2026-11-01T02:00:00Z"}}]}}`
	past := `{"error":{"message":"out","details":[{"metadata":{"quotaResetTimeStamp":"2026-10-01T02:00:00Z"}}]}}`
	delay := `{"error":{"message":"out","details":[{"retryDelay":"90.5s"}]}}`
	for name, tc := range map[string]struct {
		ag     bool
		body   string
		status int
		want   time.Time
	}{
		"cli retry info":         {false, retryInfo, 429, now.Add(30 * time.Second)},
		"cli ignores a stamp":    {false, quota, 429, time.Time{}},
		"cli ignores a 409":      {false, retryInfo, 409, time.Time{}},
		"antigravity stamp":      {true, quota, 429, time.Date(2026, 11, 1, 2, 0, 0, 0, time.UTC)},
		"antigravity on a 409":   {true, quota, 409, time.Date(2026, 11, 1, 2, 0, 0, 0, time.UTC)},
		"antigravity delay":      {true, delay, 429, now.Add(90*time.Second + 500*time.Millisecond)},
		"antigravity past stamp": {true, past, 429, time.Time{}},
		"not json":               {true, `<html>`, 429, time.Time{}},
	} {
		c := &CloudCode{Antigravity: tc.ag, Now: func() time.Time { return now }}
		out, ok := c.limited(tc.status, http.Header{}, []byte(tc.body))
		if tc.want.IsZero() {
			if ok {
				t.Errorf("%s: a reset was read from nothing", name)
			}
			continue
		}
		if !ok || !executor.ParseError(tc.status, out).ResetAt.Equal(time.Unix(tc.want.Unix(), 0)) {
			t.Errorf("%s: %s", name, out)
		}
	}
}

func TestAntigravityRetriesInPlaceOnlyWhenTheHintIsShort(t *testing.T) {
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	short := http.Header{"Retry-After": {"3"}}
	for name, tc := range map[string]struct {
		h      http.Header
		body   string
		status int
		tries  int
		want   time.Duration
		retry  bool
	}{
		"retry-after":            {short, ``, 429, 0, 3 * time.Second, true},
		"too long":               {http.Header{"Retry-After": {"11"}}, ``, 429, 0, 11 * time.Second, false},
		"exactly the ceiling":    {http.Header{"Retry-After": {"10"}}, ``, 429, 0, 10 * time.Second, true},
		"reset after text":       {http.Header{}, `quota will reset after 7s`, 429, 0, 7 * time.Second, true},
		"reset after hours":      {http.Header{}, `reset after 2h7m23s`, 429, 0, 2*time.Hour + 7*time.Minute + 23*time.Second, false},
		"rate limit reset after": {http.Header{"X-Ratelimit-Reset-After": {"4"}}, ``, 429, 0, 4 * time.Second, true},
		"backoff 429, first":     {http.Header{}, `{}`, 429, 0, time.Second, true},
		"backoff 429, third":     {http.Header{}, `{}`, 429, 2, 4 * time.Second, true},
		"backoff 429 capped":     {http.Header{}, `{}`, 429, 6, 10 * time.Second, true},
		"backoff 503 capped":     {http.Header{}, `{}`, 503, 6, 15 * time.Second, true},
		"500 transient":          {http.Header{}, `{}`, 500, 1, 2 * time.Second, true},
	} {
		got, ok := antigravityDelay(tc.h, []byte(tc.body), tc.status, tc.tries, now)
		if ok != tc.retry || (ok && got != tc.want) {
			t.Errorf("%s: %v %v", name, got, ok)
		}
	}
}

func TestAntigravityRetriesAFailureThreeTimesThenGivesTheAnswer(t *testing.T) {
	c, up, req := ccRun(t, true,
		tests.Reply{Status: 503, Body: `{"error":{"message":"high traffic"}}`},
		tests.Reply{Status: 503, Body: `{"error":{"message":"high traffic"}}`},
		tests.Reply{Status: 200, Body: `{"ok":true}`},
	)
	resp, body := ccDo(t, c, req)
	if resp.Status != 200 || body != `{"ok":true}` || len(up.Calls()) != 3 {
		t.Fatalf("%d %s, %d calls", resp.Status, body, len(up.Calls()))
	}
	c, up, req = ccRun(t, true, tests.Reply{Status: 503, Body: `{"error":{"message":"high traffic"}}`})
	if resp, _ := ccDo(t, c, req); resp.Status != 503 || len(up.Calls()) != retryTries+1 {
		t.Fatalf("%d, %d calls", resp.Status, len(up.Calls()))
	}
	c, up, req = ccRun(t, false, tests.Reply{Status: 429, Body: `x`})
	if resp, _ := ccDo(t, c, req); resp.Status != 429 || len(up.Calls()) != 1 {
		t.Fatalf("gemini-cli retried a 429 in place: %d calls", len(up.Calls()))
	}
}

func TestAnAntigravityRefusalWithALongResetIsNotRetried(t *testing.T) {
	c, up, req := ccRun(t, true, tests.Reply{Status: 429, Body: `{"error":{"message":"Your quota will reset after 2h7m23s"}}`})
	if resp, _ := ccDo(t, c, req); resp.Status != 429 || len(up.Calls()) != 1 {
		t.Fatalf("%d, %d calls", resp.Status, len(up.Calls()))
	}
}

func FuzzGoogleReset(f *testing.F) {
	for _, s := range []string{`{}`, `{"error":{"details":[{"retryDelay":"1s"}]}}`, `[`, `{"error":{"details":[{"retryDelay":"-1s"}]}}`, `{"error":{"details":[{"retryDelay":"1e308s"}]}}`} {
		f.Add([]byte(s))
	}
	now := time.Unix(1_700_000_000, 0)
	f.Fuzz(func(t *testing.T, raw []byte) {
		for _, ag := range []bool{true, false} {
			if at, ok := googleReset(raw, ag, now); ok && (!at.After(now) || at.After(now.Add(8*24*time.Hour))) {
				t.Fatalf("reset %v outside the window", at)
			}
		}
		_ = textDelay(raw)
	})
}
