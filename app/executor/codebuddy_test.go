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

func sysBody(content string) []byte {
	return []byte(`{"model":"m","messages":[{"role":"system","content":` + quoteJSON(content) + `},{"role":"user","content":"hi"}]}`)
}

func TestTheChinaGatewayReplacesAnAgentSystemPromptAndKeepsAUsersOwn(t *testing.T) {
	cn := &CodeBuddy{}
	for name, tc := range map[string]struct {
		prompt   string
		replaced bool
	}{
		"claude code":          {"You are Claude Code, Anthropic's official CLI for Claude.", true},
		"a named agent":        {"you are cursor, an editor", true},
		"an agent role":        {"You are an AI coding agent that edits files", true},
		"a tag":                {"<Role>x</Role>", true},
		"just over the limit":  {strings.Repeat("a", scrubLength+1), true},
		"exactly the limit":    {strings.Repeat("a", scrubLength), false},
		"multibyte at limit":   {strings.Repeat("é", scrubLength), false},
		"a user's own prompt":  {"Answer in French.", false},
		"an unrelated 'agent'": {"Talk like a travel agent.", false},
		"an empty prompt":      {"", false},
	} {
		out := cn.shape(sysBody(tc.prompt))
		got := gjson.GetBytes(out, "messages.0.content").String()
		if (got == neutralPrompt) != tc.replaced || (!tc.replaced && got != tc.prompt) {
			t.Errorf("%s: %q", name, got)
		}
		if gjson.GetBytes(out, "messages.1.content").String() != "hi" {
			t.Errorf("%s: the user turn changed", name)
		}
	}
}

func TestTheChinaScrubKeepsTheShapeOfTypedBlocks(t *testing.T) {
	out := (&CodeBuddy{}).shape([]byte(`{"messages":[{"role":"system","content":[{"type":"text","text":"You are Claude Code"},{"type":"text","text":"more"}]}]}`))
	c := gjson.GetBytes(out, "messages.0.content")
	if !c.IsArray() || len(c.Array()) != 1 || c.Get("0.text").String() != neutralPrompt {
		t.Fatalf("%s", out)
	}
}

func TestTheIntlGatewayGetsItsOwnSystemPromptAndTypedUserBlocks(t *testing.T) {
	in := `{"messages":[{"role":"system","content":"s"},{"role":"developer","content":"d"},{"role":"user","content":"hello \"x\""},` +
		`{"role":"user","content":[{"type":"text","text":"typed"}]},{"role":"assistant","content":"ok"},"junk"]}`
	out := (&CodeBuddy{Intl: true}).shape([]byte(in))
	m := gjson.GetBytes(out, "messages").Array()
	if len(m) != 4 || m[0].Get("role").String() != "system" || m[0].Get("content").String() != codebuddyPrompt {
		t.Fatalf("%s", out)
	}
	if m[1].Get("content.0.type").String() != "text" || m[1].Get("content.0.text").String() != `hello "x"` {
		t.Fatalf("%s", m[1].Raw)
	}
	if m[2].Get("content.0.text").String() != "typed" || m[3].Get("content").String() != "ok" {
		t.Fatalf("%s", out)
	}
}

func TestReasoningEffortIsMirroredAsASummaryOrDropped(t *testing.T) {
	for _, intl := range []bool{false, true} {
		g := &CodeBuddy{Intl: intl}
		for effort, want := range map[string][2]string{
			"high": {"high", "auto"}, "low": {"low", "auto"}, "none": {"", ""}, "off": {"", ""},
		} {
			out := g.shape([]byte(`{"reasoning_effort":"` + effort + `","messages":[]}`))
			if gjson.GetBytes(out, "reasoning_effort").String() != want[0] || gjson.GetBytes(out, "reasoning_summary").String() != want[1] {
				t.Errorf("intl=%v %s: %s", intl, effort, out)
			}
		}
		if out := g.shape([]byte(`{"messages":[]}`)); gjson.GetBytes(out, "reasoning_summary").Exists() {
			t.Errorf("a summary was added to a request that asked for no reasoning: %s", out)
		}
	}
	if out := string((&CodeBuddy{}).shape([]byte(`not json`))); out != "not json" {
		t.Fatal(out)
	}
}

func TestACodeBuddyCallCarriesFreshIDsAndTheCredential(t *testing.T) {
	up := tests.NewUpstream(t, tests.Reply{Body: `{}`}, tests.Reply{Body: `{}`})
	g := &CodeBuddy{Default: &Default{Client: http.DefaultClient}}
	for range 2 {
		resp, err := g.Execute(context.Background(), Request{
			URL: up.URL, Body: sysBody("x"), Key: "TOK",
			Auth: &upstream.Auth{Scheme: upstream.SchemeBearer, Header: "Authorization", Headers: map[string]string{"X-Product": "SaaS"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}
	a, b := up.Calls()[0].Header, up.Calls()[1].Header
	if a.Get("Authorization") != "Bearer TOK" || a.Get("X-Product") != "SaaS" {
		t.Fatalf("%v", a)
	}
	id := a.Get("X-Request-Id")
	if id == "" || id == b.Get("X-Request-Id") || a.Get("X-Conversation-Request-Id") != id || a.Get("X-Conversation-Message-Id") != id ||
		a.Get("X-Conversation-Id") == "" || a.Get("X-Conversation-Id") == id {
		t.Fatalf("%v", a)
	}
}

func TestACodeBuddyCallWithoutACredentialSendsNothing(t *testing.T) {
	up := tests.NewUpstream(t)
	g := &CodeBuddy{Default: &Default{Client: http.DefaultClient}}
	if _, err := g.Execute(context.Background(), Request{URL: up.URL, Body: sysBody("x")}); err != ErrNoCredential || len(up.Calls()) != 0 {
		t.Fatalf("%v, %d", err, len(up.Calls()))
	}
}

func TestTheFrequencyLimitBecomesA429WithTheResetTime(t *testing.T) {
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	g := &CodeBuddy{Now: func() time.Time { return now }}
	for name, tc := range map[string]struct {
		body  string
		limit bool
		reset time.Time
	}{
		"code with date, default zone": {`{"code":6004,"msg":"超出频率限制，请于 2026-11-02 08:00:00 后重试"}`, true, time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)},
		"explicit zone":                {`{"code":6004,"msg":"limit until 2026-11-02 08:00:00 UTC+8"}`, true, time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)},
		"zone with minutes":            {`{"msg":"frequency limit 2026-11-02 08:00:00 UTC+5:30"}`, true, time.Date(2026, 11, 2, 2, 30, 0, 0, time.UTC)},
		"words only":                   {`{"message":"frequency limit reached"}`, true, time.Time{}},
		"a past date":                  {`{"code":6004,"msg":"x 2026-10-01 00:00:00"}`, true, time.Time{}},
		"an absurd zone":               {`{"code":6004,"msg":"x 2026-11-02 08:00:00 UTC+99"}`, true, time.Time{}},
		"code only":                    {`{"code":6004}`, true, time.Time{}},
		"another code":                 {`{"code":11101,"msg":"bad request"}`, false, time.Time{}},
		"not json":                     {`<html>limit</html>`, false, time.Time{}},
	} {
		out, ok := g.limited([]byte(tc.body))
		if ok != tc.limit {
			t.Errorf("%s: limited=%v", name, ok)
			continue
		}
		if !ok {
			continue
		}
		pe := ParseError(429, out)
		if !pe.ResetAt.Equal(tc.reset) && !(tc.reset.IsZero() && pe.ResetAt.IsZero()) {
			t.Errorf("%s: reset %v, want %v", name, pe.ResetAt, tc.reset)
		}
		if pe.Message == "" {
			t.Errorf("%s: no message", name)
		}
	}
}

func TestALimitAnswerLeavesThePipelineAs429AndOtherErrorsKeepTheirStatus(t *testing.T) {
	up := tests.NewUpstream(t,
		tests.Reply{Status: 400, Body: `{"code":6004,"msg":"超出频率限制"}`},
		tests.Reply{Status: 400, Body: `{"code":11101,"msg":"Non-stream"}`},
		tests.Reply{Status: 200, Body: `{"ok":true}`},
	)
	g := &CodeBuddy{Default: &Default{Client: http.DefaultClient}}
	do := func() (Response, string) {
		resp, err := g.Execute(context.Background(), Request{URL: up.URL, Body: sysBody("x"), Key: "K", Auth: &upstream.Auth{Scheme: upstream.SchemeBearer, Header: "Authorization"}})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return resp, string(raw)
	}
	if resp, body := do(); resp.Status != 429 || !strings.Contains(body, "频率") {
		t.Fatalf("%d %s", resp.Status, body)
	}
	if resp, body := do(); resp.Status != 400 || !strings.Contains(body, "Non-stream") {
		t.Fatalf("%d %s", resp.Status, body)
	}
	if resp, body := do(); resp.Status != 200 || body != `{"ok":true}` {
		t.Fatalf("a good answer was touched: %d %s", resp.Status, body)
	}
}

func FuzzCodeBuddyLimit(f *testing.F) {
	for _, s := range []string{`{}`, `{"code":6004}`, `{"msg":"frequency limit 2026-01-01 00:00:00 UTC+8"}`, `[`, `{"code":6004,"msg":"x 9999-99-99 99:99:99 UTC+:"}`} {
		f.Add([]byte(s))
	}
	now := time.Unix(1_700_000_000, 0)
	g := &CodeBuddy{Now: func() time.Time { return now }}
	f.Fuzz(func(t *testing.T, raw []byte) {
		out, ok := g.limited(raw)
		if !ok {
			return
		}
		pe := ParseError(429, out)
		if !pe.ResetAt.IsZero() && (!pe.ResetAt.After(now) || pe.ResetAt.After(now.Add(maxLimitReset))) {
			t.Fatalf("reset %v outside the window", pe.ResetAt)
		}
	})
}
