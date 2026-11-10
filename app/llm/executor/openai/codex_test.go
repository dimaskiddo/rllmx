package openai

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/executor"
	"rllmx/app/llm/upstream"
	"rllmx/tests"
	"rllmx/tests/llm"
)

const codexIn = `{"model":"gpt-5.6-sol","instructions":"be brief","stream":false,"store":true,"temperature":0.5,"max_output_tokens":99,` +
	`"user":"u","metadata":{"a":1},"previous_response_id":"resp_1","prompt_cache_key":"client-chosen","service_tier":"fast",` +
	`"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],` +
	`"tools":[{"type":"function","function":{"name":"read","parameters":{"type":"object","properties":{"a":{"type":"string","pattern":"\\p{L}"}}}}},{"type":"web_search"}]}`

func codexReq(body string, mod func(*executor.Request)) executor.Request {
	r := executor.Request{Body: []byte(body), Session: "keyed-session", Levels: []string{"low", "medium", "high", "xhigh"}}
	if mod != nil {
		mod(&r)
	}
	return r
}

func TestCodexKeepsOnlyTheFieldsTheBackendTakes(t *testing.T) {
	out, _ := shapeCodex(codexReq(codexIn, nil))
	allowed := map[string]bool{}
	for k := range codexFields {
		allowed[k] = true
	}
	gjson.ParseBytes(out).ForEach(func(k, _ gjson.Result) bool {
		if !allowed[k.String()] {
			t.Errorf("field %q reached the backend", k.String())
		}
		return true
	})
	for _, k := range []string{"temperature", "max_output_tokens", "user", "metadata", "previous_response_id"} {
		if gjson.GetBytes(out, k).Exists() {
			t.Errorf("%s was sent", k)
		}
	}
}

func TestCodexAlwaysStreamsAndNeverStores(t *testing.T) {
	out, _ := shapeCodex(codexReq(codexIn, nil))
	if !gjson.GetBytes(out, "stream").Bool() || gjson.GetBytes(out, "store").Bool() {
		t.Fatalf("%s", out)
	}
}

func TestTheCacheKeyIsTheKeyedSessionNeverTheClients(t *testing.T) {
	out, _ := shapeCodex(codexReq(codexIn, nil))
	if got := gjson.GetBytes(out, "prompt_cache_key").String(); got != "keyed-session" {
		t.Fatalf("prompt_cache_key = %q", got)
	}
	out, _ = shapeCodex(codexReq(codexIn, func(r *executor.Request) { r.Session = "" }))
	if gjson.GetBytes(out, "prompt_cache_key").Exists() {
		t.Fatalf("the client's cache key reached the backend: %s", out)
	}
}

func TestOurOwnInstructionsFillAnEmptySlotOnly(t *testing.T) {
	out, _ := shapeCodex(codexReq(strings.Replace(codexIn, `"instructions":"be brief"`, `"instructions":"  "`, 1), nil))
	if got := gjson.GetBytes(out, "instructions").String(); got == "" || got == "  " || strings.Contains(got, "Codex CLI") {
		t.Fatalf("instructions = %q", got)
	}
	out, _ = shapeCodex(codexReq(codexIn, nil))
	if gjson.GetBytes(out, "instructions").String() != "be brief" {
		t.Fatalf("the client's instructions changed: %s", gjson.GetBytes(out, "instructions").Raw)
	}
}

func TestServiceTier(t *testing.T) {
	for in, want := range map[string]string{`"fast"`: "priority", `"priority"`: "priority", `"flex"`: "", `""`: ""} {
		out, _ := shapeCodex(codexReq(strings.Replace(codexIn, `"service_tier":"fast"`, `"service_tier":`+in, 1), nil))
		if got := gjson.GetBytes(out, "service_tier").String(); got != want {
			t.Errorf("%s: %q, want %q", in, got, want)
		}
	}
}

func TestCodexEffortFollowsTheModelsLevels(t *testing.T) {
	cases := []struct {
		name, effort string
		levels       []string
		lite         bool
		want         string
	}{
		{"declared level kept", "high", []string{"low", "high"}, false, "high"},
		{"none stays on a full model", "none", []string{"low"}, false, "none"},
		{"none becomes low on Lite", "none", []string{"low", "medium"}, true, "low"},
		{"minimal becomes low on Lite", "minimal", []string{"low", "medium"}, true, "low"},
		{"max becomes xhigh", "max", []string{"low", "xhigh"}, false, "xhigh"},
		{"max kept when declared", "max", []string{"low", "max"}, false, "max"},
		{"ultra becomes max when declared", "ultra", []string{"low", "max"}, false, "max"},
		{"ultra becomes xhigh otherwise", "ultra", []string{"low", "xhigh"}, false, "xhigh"},
	}
	for _, c := range cases {
		body := `{"model":"m","input":[],"reasoning":{"effort":"` + c.effort + `"}}`
		out, _ := shapeCodex(codexReq(body, func(r *executor.Request) { r.Levels = c.levels; r.Spec.Lite = c.lite }))
		if got := gjson.GetBytes(out, "reasoning.effort").String(); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestDefaultEffortAndTheEncryptedReasoningInclude(t *testing.T) {
	out, _ := shapeCodex(codexReq(`{"model":"m","input":[]}`, nil))
	if gjson.GetBytes(out, "reasoning.effort").String() != "low" || gjson.GetBytes(out, "reasoning.summary").String() != "auto" {
		t.Fatalf("%s", gjson.GetBytes(out, "reasoning").Raw)
	}
	if gjson.GetBytes(out, "include.0").String() != "reasoning.encrypted_content" {
		t.Fatalf("include: %s", out)
	}
	none, _ := shapeCodex(codexReq(`{"model":"m","input":[],"include":["x"],"reasoning":{"effort":"none"}}`, nil))
	if gjson.GetBytes(none, "include").Exists() {
		t.Fatalf("include sent with no reasoning: %s", none)
	}
}

func TestLiteMovesToolsAndInstructionsIntoTheInput(t *testing.T) {
	out, lite := shapeCodex(codexReq(strings.Replace(codexIn, `{"type":"web_search"}`, `{"type":"function","name":"w"}`, 1), func(r *executor.Request) { r.Spec.Lite = true }))
	if !lite {
		t.Fatal("not Lite")
	}
	if gjson.GetBytes(out, "input.0.type").String() != "additional_tools" || len(gjson.GetBytes(out, "input.0.tools").Array()) != 2 {
		t.Fatalf("input prefix: %s", gjson.GetBytes(out, "input").Raw)
	}
	if gjson.GetBytes(out, "input.1.role").String() != "developer" || gjson.GetBytes(out, "input.1.content.0.text").String() != "be brief" {
		t.Fatalf("instruction message: %s", gjson.GetBytes(out, "input.1").Raw)
	}
	if gjson.GetBytes(out, "input.2.role").String() != "user" {
		t.Fatalf("the conversation moved: %s", gjson.GetBytes(out, "input").Raw)
	}
	if gjson.GetBytes(out, "instructions").String() != "" || gjson.GetBytes(out, "tools").Type != gjson.Null {
		t.Fatalf("tools/instructions not emptied: %s", out)
	}
	if gjson.GetBytes(out, "parallel_tool_calls").Bool() || gjson.GetBytes(out, "tool_choice").String() != "auto" {
		t.Fatalf("%s", out)
	}
	if gjson.GetBytes(out, "reasoning.context").String() != "all_turns" || gjson.GetBytes(out, "reasoning.summary").Exists() {
		t.Fatalf("%s", gjson.GetBytes(out, "reasoning").Raw)
	}
	if gjson.GetBytes(out, "reasoning.effort").String() != "medium" {
		t.Fatalf("Lite defaults to medium: %s", gjson.GetBytes(out, "reasoning").Raw)
	}
}

func TestAWebSearchToolTurnsLiteOff(t *testing.T) {
	out, lite := shapeCodex(codexReq(codexIn, func(r *executor.Request) { r.Spec.Lite = true }))
	if lite || gjson.GetBytes(out, "input.0.type").String() == "additional_tools" {
		t.Fatalf("Lite with a hosted search tool: %s", out)
	}
}

func TestToolSchemasLoseOnlyTheirUnicodePatterns(t *testing.T) {
	out, _ := shapeCodex(codexReq(codexIn, nil))
	if strings.Contains(gjson.GetBytes(out, "tools.0.parameters").Raw, "pattern") {
		t.Fatalf("%s", gjson.GetBytes(out, "tools.0.parameters").Raw)
	}
	if gjson.GetBytes(out, "tools.0.function").Exists() || gjson.GetBytes(out, "tools.0.name").String() != "read" {
		t.Fatalf("tool not flattened: %s", gjson.GetBytes(out, "tools.0").Raw)
	}
}

func FuzzShapeCodex(f *testing.F) {
	for _, s := range []string{codexIn, `{}`, `{"input":"x"}`, `{"tools":[1,"a",{}]}`, `[]`, ``, `{"reasoning":"x"}`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if !gjson.Valid(s) || !gjson.Parse(s).IsObject() {
			return
		}
		out, _ := shapeCodex(codexReq(s, nil))
		if !gjson.ValidBytes(out) {
			t.Fatalf("invalid JSON out for %q: %s", s, out)
		}
		gjson.ParseBytes(out).ForEach(func(k, _ gjson.Result) bool {
			if !codexFields[k.String()] {
				t.Fatalf("field %q escaped the allowlist", k.String())
			}
			return true
		})
	})
}

func codexExec(t *testing.T, idle time.Duration, waits *[]time.Duration) *Codex {
	t.Helper()
	return &Codex{Default: &executor.Default{Client: http.DefaultClient, Sleep: tests.InstantSleep(waits)}, PeekIdle: idle}
}

func runCodex(t *testing.T, c *Codex, url string, mod func(*executor.Request)) (executor.Response, error) {
	t.Helper()
	req := executor.Request{URL: url, Key: llm.Secret, Auth: llm.Bearer(), Body: []byte(`{"model":"m","input":[]}`), Stream: true}
	if mod != nil {
		mod(&req)
	}
	return c.Execute(context.Background(), req)
}

func sse(body string) tests.Reply {
	return tests.Reply{ContentType: "text/event-stream", Body: body}
}

const (
	okStream = "event: response.created\ndata: {\"type\":\"response.created\"}\n\nevent: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n"
	busyBody = "event: error\ndata: {\"error\":{\"code\":\"server_is_overloaded\",\"message\":\"slow\"}}\n\n"
	fullBody = "event: error\ndata: {\"error\":{\"message\":\"Selected model is at capacity. Please try a different model.\"}}\n\n"
)

func TestCompactAddsThePathOnlyForACompactCall(t *testing.T) {
	up := tests.NewUpstream(t, sse(okStream), sse(okStream))
	var w []time.Duration
	c := codexExec(t, 0, &w)
	for _, compact := range []bool{false, true} {
		resp, err := runCodex(t, c, up.URL+"/backend/responses", func(r *executor.Request) { r.Compact = compact })
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	calls := up.Calls()
	if calls[0].Path != "/backend/responses" || calls[1].Path != "/backend/responses/compact" {
		t.Fatalf("paths %s %s", calls[0].Path, calls[1].Path)
	}
}

func TestTwoCallsAtOnceNeverShareTheCompactFlag(t *testing.T) {
	up := tests.NewUpstream(t, sse(okStream))
	var w []time.Duration
	c := codexExec(t, 0, &w)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(compact bool) {
			defer wg.Done()
			resp, err := runCodex(t, c, up.URL+"/r", func(r *executor.Request) { r.Compact = compact })
			if err == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
		}(i%2 == 0)
	}
	wg.Wait()
	compact := 0
	for _, call := range up.Calls() {
		if strings.HasSuffix(call.Path, "/compact") {
			compact++
		}
	}
	if compact != 10 {
		t.Fatalf("%d compact calls of 20, want 10", compact)
	}
}

func TestTheLiteHeaderGoesOnlyWithALiteCall(t *testing.T) {
	up := tests.NewUpstream(t, sse(okStream), sse(okStream))
	var w []time.Duration
	c := codexExec(t, 0, &w)
	for _, lite := range []bool{true, false} {
		resp, err := runCodex(t, c, up.URL+"/r", func(r *executor.Request) { r.Spec.Lite = lite })
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	calls := up.Calls()
	if calls[0].Header.Get(liteHeader) != "true" || calls[1].Header.Get(liteHeader) != "" {
		t.Fatalf("headers %v / %v", calls[0].Header, calls[1].Header)
	}
}

func TestACleanAnswerIsReplayedByteForByte(t *testing.T) {
	up := tests.NewUpstream(t, tests.Reply{ContentType: "text/event-stream", Chunks: []string{okStream[:30], okStream[30:90], okStream[90:]}})
	var w []time.Duration
	resp, err := runCodex(t, codexExec(t, 0, &w), up.URL+"/r", nil)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(got) != okStream {
		t.Fatalf("replay differs:\n%q\n%q", got, okStream)
	}
}

func TestAnOverloadedAnswerInsideA200IsRetriedOnTheSameAccount(t *testing.T) {
	up := tests.NewUpstream(t, sse(busyBody), sse(busyBody), sse(okStream))
	var w []time.Duration
	resp, err := runCodex(t, codexExec(t, 0, &w), up.URL+"/r", nil)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.Status != 200 || string(got) != okStream || len(up.Calls()) != 3 {
		t.Fatalf("status %d after %d calls: %q", resp.Status, len(up.Calls()), got)
	}
	if len(w) != 2 {
		t.Fatalf("waited %v", w)
	}
}

func TestRetriesRunOutAs503WithAFixedMessage(t *testing.T) {
	up := tests.NewUpstream(t, sse(busyBody))
	var w []time.Duration
	resp, err := runCodex(t, codexExec(t, 0, &w), up.URL+"/r", nil)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.Status != 503 || strings.Contains(string(got), "slow") || len(up.Calls()) != 4 {
		t.Fatalf("status %d after %d calls: %s", resp.Status, len(up.Calls()), got)
	}
}

func TestACapacityAnswerIsAnImmediate503WithNoRetry(t *testing.T) {
	up := tests.NewUpstream(t, sse(fullBody), sse(okStream))
	var w []time.Duration
	resp, err := runCodex(t, codexExec(t, 0, &w), up.URL+"/r", nil)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.Status != 503 || len(up.Calls()) != 1 || !strings.Contains(string(got), "at capacity") {
		t.Fatalf("status %d after %d calls: %s", resp.Status, len(up.Calls()), got)
	}
}

func TestAnErrorAfterRealOutputIsNeverRetried(t *testing.T) {
	up := tests.NewUpstream(t, sse(okStream+busyBody), sse(okStream))
	var w []time.Duration
	resp, err := runCodex(t, codexExec(t, 0, &w), up.URL+"/r", nil)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.Status != 200 || len(up.Calls()) != 1 || string(got) != okStream+busyBody {
		t.Fatalf("status %d after %d calls: %q", resp.Status, len(up.Calls()), got)
	}
}

func TestAPatternSplitAcrossTwoChunksIsStillFound(t *testing.T) {
	cut := strings.Index(busyBody, "overloaded") + 4
	up := tests.NewUpstream(t, tests.Reply{ContentType: "text/event-stream", Chunks: []string{busyBody[:cut], busyBody[cut:]}}, sse(okStream))
	var w []time.Duration
	resp, err := runCodex(t, codexExec(t, 0, &w), up.URL+"/r", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(up.Calls()) != 2 || resp.Status != 200 {
		t.Fatalf("%d calls, status %d", len(up.Calls()), resp.Status)
	}
}

func TestAQuietUpstreamEndsThePeekAndHandsOverWhatWasRead(t *testing.T) {
	up := tests.NewUpstream(t, tests.Reply{ContentType: "text/event-stream", Chunks: []string{"event: response.created\ndata: {}\n\n", "event: later\ndata: {}\n\n"}, Pause: 600 * time.Millisecond})
	var w []time.Duration
	start := time.Now()
	resp, err := runCodex(t, codexExec(t, 100*time.Millisecond, &w), up.URL+"/r", nil)
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 450*time.Millisecond {
		t.Fatalf("the peek waited %v for a quiet upstream", took)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(got), "response.created") || !strings.Contains(string(got), "event: later") {
		t.Fatalf("bytes lost across the idle bound: %q", got)
	}
}

func TestAnErrorStatusIsReturnedUntouched(t *testing.T) {
	up := tests.NewUpstream(t, tests.Reply{Status: 429, Body: `{"error":{"type":"usage_limit_reached","resets_in_seconds":120}}`})
	var w []time.Duration
	resp, err := runCodex(t, codexExec(t, 0, &w), up.URL+"/r", func(r *executor.Request) { r.Entry = upstream.Entry{} })
	if err != nil {
		t.Fatal(err)
	}
	e := executor.ReadError(resp.Status, resp.Body)
	resp.Body.Close()
	if resp.Status != 429 || e.ResetAt.IsZero() || time.Until(e.ResetAt) < 100*time.Second {
		t.Fatalf("status %d, reset %v", resp.Status, e.ResetAt)
	}
}

func TestNoKeyIsRefusedBeforeAnything(t *testing.T) {
	up := tests.NewUpstream(t, sse(okStream))
	var w []time.Duration
	_, err := runCodex(t, codexExec(t, 0, &w), up.URL+"/r", func(r *executor.Request) { r.Key = "" })
	if err != executor.ErrNoCredential || len(up.Calls()) != 0 {
		t.Fatalf("err %v, %d calls", err, len(up.Calls()))
	}
}
