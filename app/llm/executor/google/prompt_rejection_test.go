package google

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/executor"
	"rllmx/tests"
)

const bareExhausted = `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Resource has been exhausted (e.g. check quota)."}}`

const withSystem = `{"project":"","model":"claude-opus-5-5","requestId":"","request":{"systemInstruction":{"role":"user","parts":[{"text":"SYS ONE"},{"text":"SYS TWO"}]},"contents":[{"role":"user","parts":[{"text":"hi"}]},{"role":"model","parts":[{"text":"yo"}]}]}}`

func rejectionRun(t *testing.T, body, model string, replies ...tests.Reply) (*CloudCode, *tests.Upstream, executor.Request) {
	t.Helper()
	c, up, req := ccRun(t, true, replies...)
	req.Body, req.Model = []byte(body), model
	return c, up, req
}

func TestABareExhaustionIsNotRetriedInPlace(t *testing.T) {
	c, up, req := rejectionRun(t, envelope, "gemini-3-pro-preview", tests.Reply{Status: 429, Body: bareExhausted})
	resp, body := ccDo(t, c, req)
	if resp.Status != 400 || len(up.Calls()) != 1 {
		t.Fatalf("%d after %d calls: %s", resp.Status, len(up.Calls()), body)
	}
	if msg := executor.ParseError(resp.Status, []byte(body)).Message; !strings.Contains(msg, "not your quota") {
		t.Errorf("the refusal does not say it is not the quota: %q", msg)
	}
}

func TestARejectedPromptIsSentAgainWithTheSystemTextInTheFirstTurn(t *testing.T) {
	c, up, req := rejectionRun(t, withSystem, "claude-opus-5-5",
		tests.Reply{Status: 429, Body: bareExhausted},
		tests.Reply{Status: 200, Body: `{"ok":true}`},
	)
	resp, body := ccDo(t, c, req)
	if resp.Status != 200 || body != `{"ok":true}` || len(up.Calls()) != 2 {
		t.Fatalf("%d after %d calls: %s", resp.Status, len(up.Calls()), body)
	}
	first, second := up.Calls()[0].Body, up.Calls()[1].Body
	if !gjson.GetBytes(first, "request.systemInstruction").Exists() {
		t.Error("the first call lost its system instruction")
	}
	if gjson.GetBytes(second, "request.systemInstruction").Exists() {
		t.Errorf("the second call still carries a system instruction: %s", second)
	}
	got := gjson.GetBytes(second, "request.contents.0")
	if got.Get("role").String() != "user" || got.Get("parts.0.text").String() != "SYS ONE" ||
		got.Get("parts.1.text").String() != "SYS TWO" || got.Get("parts.2.text").String() != "hi" {
		t.Errorf("the system text is not at the front of the first user turn: %s", got.Raw)
	}
}

func TestARealQuotaErrorKeepsItsRetryAndLock(t *testing.T) {
	real := `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Resource has been exhausted (e.g. check quota).","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","metadata":{"quotaResetTimeStamp":"2026-11-01T01:00:00Z"}}]}}`
	c, up, req := rejectionRun(t, withSystem, "claude-opus-5-5", tests.Reply{Status: 429, Body: real})
	resp, body := ccDo(t, c, req)
	if resp.Status != 429 || len(up.Calls()) != retryTries+1 {
		t.Fatalf("%d after %d calls: %s", resp.Status, len(up.Calls()), body)
	}
	if executor.ParseError(resp.Status, []byte(body)).ResetAt.IsZero() {
		t.Errorf("the reset the lock reads is gone: %s", body)
	}
	for i, call := range up.Calls() {
		if !gjson.GetBytes(call.Body, "request.systemInstruction").Exists() {
			t.Errorf("call %d: a real quota error must not move the system text", i)
		}
	}
}

func TestARealQuotaErrorWithAShortHintIsStillRetriedInPlace(t *testing.T) {
	real := `{"error":{"status":"RESOURCE_EXHAUSTED","message":"Resource has been exhausted (e.g. check quota).","details":[{"@type":"x"}]}}`
	c, up, req := rejectionRun(t, withSystem, "claude-opus-5-5",
		tests.Reply{Status: 429, Body: real, Header: map[string]string{"Retry-After": "1"}},
		tests.Reply{Status: 200, Body: `{"ok":true}`},
	)
	resp, _ := ccDo(t, c, req)
	if resp.Status != 200 || len(up.Calls()) != 2 {
		t.Fatalf("%d after %d calls", resp.Status, len(up.Calls()))
	}
	if !gjson.GetBytes(up.Calls()[1].Body, "request.systemInstruction").Exists() {
		t.Error("the retry moved the system text although the refusal named a quota")
	}
}

func TestAnImageRequestIsNotRewrittenOnRejection(t *testing.T) {
	c, up, req := rejectionRun(t, withSystem, "gemini-3-pro-image", tests.Reply{Status: 429, Body: bareExhausted})
	resp, _ := ccDo(t, c, req)
	if resp.Status != 400 || len(up.Calls()) != 1 {
		t.Fatalf("%d after %d calls", resp.Status, len(up.Calls()))
	}
}

func TestASecondRejectionIsAnAnswerNotALock(t *testing.T) {
	c, up, req := rejectionRun(t, withSystem, "claude-opus-5-5", tests.Reply{Status: 429, Body: bareExhausted})
	resp, body := ccDo(t, c, req)
	if resp.Status != 400 || len(up.Calls()) != 2 {
		t.Fatalf("%d after %d calls: %s", resp.Status, len(up.Calls()), body)
	}
	if gjson.Get(body, "error.status").String() == "RESOURCE_EXHAUSTED" || strings.Contains(strings.ToLower(body), "rate limit") {
		t.Errorf("the answer still reads as a limit: %s", body)
	}
}

func TestGeminiCLIKeepsAnyBareExhaustionAs429(t *testing.T) {
	c, up, req := ccRun(t, false, tests.Reply{Status: 429, Body: bareExhausted})
	req.Body = []byte(withSystem)
	if resp, _ := ccDo(t, c, req); resp.Status != 429 || len(up.Calls()) != 1 {
		t.Fatalf("%d after %d calls", resp.Status, len(up.Calls()))
	}
}

func TestPromptRejectedTable(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
		want   bool
	}{
		"the bare body":        {429, bareExhausted, true},
		"no trailing dot":      {429, `{"error":{"status":"RESOURCE_EXHAUSTED","message":"Resource has been exhausted (e.g. check quota)"}}`, true},
		"empty details":        {429, `{"error":{"status":"RESOURCE_EXHAUSTED","message":"Resource has been exhausted (e.g. check quota).","details":[]}}`, true},
		"with details":         {429, `{"error":{"status":"RESOURCE_EXHAUSTED","message":"Resource has been exhausted (e.g. check quota).","details":[{"a":1}]}}`, false},
		"another message":      {429, `{"error":{"status":"RESOURCE_EXHAUSTED","message":"Quota exceeded for model"}}`, false},
		"a longer message":     {429, `{"error":{"status":"RESOURCE_EXHAUSTED","message":"Resource has been exhausted (e.g. check quota). Resets in 1h"}}`, false},
		"another status":       {429, `{"error":{"status":"UNAVAILABLE","message":"Resource has been exhausted (e.g. check quota)."}}`, false},
		"a 503":                {503, bareExhausted, false},
		"not json":             {429, `Resource has been exhausted (e.g. check quota).`, false},
		"empty":                {429, ``, false},
		"message in a 409":     {409, bareExhausted, false},
		"padded message":       {429, `{"error":{"status":"RESOURCE_EXHAUSTED","message":"  Resource has been exhausted (e.g. check quota).  "}}`, true},
		"details is an object": {429, `{"error":{"status":"RESOURCE_EXHAUSTED","message":"Resource has been exhausted (e.g. check quota).","details":{"a":1}}}`, false},
	} {
		if got := promptRejected(tc.status, []byte(tc.body)); got != tc.want {
			t.Errorf("%s: got %v", name, got)
		}
	}
}

func TestMoveSystemMergesIntoAFirstUserTurnAndLeavesItsInputAlone(t *testing.T) {
	in := []byte(withSystem)
	before := string(in)
	out, ok := moveSystem(in)
	if !ok || string(in) != before {
		t.Fatalf("moved %v, input changed %v", ok, string(in) != before)
	}
	if gjson.GetBytes(out, "request.contents.#").Int() != 2 {
		t.Errorf("a user-first history must not grow: %s", out)
	}
}

func TestMoveSystemOpensANewUserTurnWhenTheHistoryStartsWithTheModel(t *testing.T) {
	out, ok := moveSystem([]byte(`{"request":{"systemInstruction":{"parts":[{"text":"S"}]},"contents":[{"role":"model","parts":[{"text":"m"}]}]}}`))
	if !ok || gjson.GetBytes(out, "request.contents.#").Int() != 2 || gjson.GetBytes(out, "request.contents.0.role").String() != "user" ||
		gjson.GetBytes(out, "request.contents.0.parts.0.text").String() != "S" || gjson.GetBytes(out, "request.contents.1.role").String() != "model" {
		t.Errorf("%v %s", ok, out)
	}
}

func TestMoveSystemRefusesWhenThereIsNothingToMove(t *testing.T) {
	for name, body := range map[string]string{
		"no system":     envelope,
		"blank text":    `{"request":{"systemInstruction":{"parts":[{"text":"  "}]},"contents":[]}}`,
		"no text parts": `{"request":{"systemInstruction":{"parts":[{"inlineData":{}}]},"contents":[]}}`,
		"not json":      `[`,
	} {
		if _, ok := moveSystem([]byte(body)); ok {
			t.Errorf("%s: moved", name)
		}
	}
}

func FuzzPromptRejected(f *testing.F) {
	for _, s := range []string{bareExhausted, `{}`, `[`, `{"error":null}`, `{"error":{"details":7}}`} {
		f.Add(429, []byte(s))
	}
	f.Fuzz(func(t *testing.T, status int, raw []byte) {
		promptRejected(status, raw)
		moveSystem(raw)
	})
}
