package aws

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/executor"
	"rllmx/app/llm/stream"
	"rllmx/app/llm/translate"
	"rllmx/app/llm/translate/kiro"
	"rllmx/app/llm/upstream"
	"rllmx/app/platform/settings"
	"rllmx/tests"
)

const rateExceededBody = `{"message":"Too many requests","reason":"USER_REQUEST_RATE_EXCEEDED"}`

// crBody is a conversation as the Kiro codec writes it: a tool exchange with an
// image, a thinking request and a tool list.
const crBody = `{"conversationState":{"chatTriggerType":"MANUAL","conversationId":"conv-1","currentMessage":{"userInputMessage":{
	"content":"and now?","modelId":"claude-sonnet-4.5","origin":"AI_EDITOR",
	"images":[{"format":"png","source":{"bytes":"QUJD"}}],
	"userInputMessageContext":{"toolResults":[{"toolUseId":"t1","status":"success","content":[{"text":"sunny"},{"text":"warm"}]}],
	"tools":[{"toolSpecification":{"name":"weather","description":"Get weather","inputSchema":{"json":{"type":"object"}}}}]}}},
	"history":[{"userInputMessage":{"content":"hi","modelId":"claude-sonnet-4.5","origin":"AI_EDITOR"}},
	{"assistantResponseMessage":{"content":"checking","toolUses":[{"toolUseId":"t1","name":"weather","input":{"city":"Paris"}}]}}]},
	"additionalModelRequestFields":{"thinking":{"type":"adaptive","display":"summarized"},"output_config":{"effort":"high"}}}`

func TestACreateResponseRequestCarriesTheConversationToolsAndThinking(t *testing.T) {
	out := buildCreateResponse([]byte(crBody), "fallback-model", "arn:aws:codewhisperer:us-east-1:1:profile/P")
	g := gjson.ParseBytes(out)
	if g.Get("model").String() != "claude-sonnet-4.5" || !g.Get("stream").Bool() || g.Get("origin").String() != "AI_EDITOR" {
		t.Errorf("envelope: %s", out)
	}
	if g.Get("profileArn").String() == "" || g.Get("metadata.kiro:conversation_id").String() != "conv-1" || g.Get("metadata.kiro:agent_mode").String() != "vibe" {
		t.Errorf("metadata: %s", out)
	}
	in := g.Get("input").Array()
	if len(in) != 5 {
		t.Fatalf("%d input items, want hi, the assistant text, its call, the call's output and the new turn: %s", len(in), g.Get("input").Raw)
	}
	if in[0].Get("role").String() != "user" || in[0].Get("content").String() != "hi" {
		t.Errorf("first turn: %s", in[0].Raw)
	}
	if in[1].Get("role").String() != "assistant" || in[1].Get("content").String() != "checking" {
		t.Errorf("assistant text: %s", in[1].Raw)
	}
	if in[2].Get("type").String() != "function_call" || in[2].Get("call_id").String() != "t1" || in[2].Get("name").String() != "weather" || gjson.Get(in[2].Get("arguments").String(), "city").String() != "Paris" {
		t.Errorf("call: %s", in[2].Raw)
	}
	if in[3].Get("type").String() != "function_call_output" || in[3].Get("call_id").String() != "t1" || in[3].Get("output").String() != "sunny\nwarm" {
		t.Errorf("call output: %s", in[3].Raw)
	}
	last := in[4].Get("content")
	if in[4].Get("role").String() != "user" || last.Get("0.text").String() != "and now?" || last.Get("1.image_url").String() != "data:image/png;base64,QUJD" {
		t.Errorf("new turn with its image: %s", in[4].Raw)
	}
	if tool := g.Get("tools.0"); tool.Get("type").String() != "function" || tool.Get("name").String() != "weather" || tool.Get("parameters.type").String() != "object" {
		t.Errorf("tools: %s", g.Get("tools").Raw)
	}
	if g.Get("reasoning.type").String() != "adaptive" || g.Get("reasoning.effort").String() != "high" || g.Get("reasoning.summary").String() != "summarized" {
		t.Errorf("reasoning: %s", g.Get("reasoning").Raw)
	}
}

func TestACreateResponseRequestFallsBackToTheCallersModelAndOmitsWhatIsEmpty(t *testing.T) {
	out := buildCreateResponse([]byte(`{"conversationState":{"currentMessage":{"userInputMessage":{"content":"x"}}}}`), "the-model", "")
	g := gjson.ParseBytes(out)
	if g.Get("model").String() != "the-model" || g.Get("tools").Exists() || g.Get("reasoning").Exists() || g.Get("profileArn").Exists() {
		t.Errorf("%s", out)
	}
	if g.Get("origin").String() != "AI_EDITOR" {
		t.Errorf("origin: %s", out)
	}
	if !gjson.ValidBytes(buildCreateResponse([]byte(`not json`), "m", "")) {
		t.Error("a body that is not json must still make a valid request")
	}
}

func TestACreateResponseURLUsesOnlyARuntimeRegion(t *testing.T) {
	for region, want := range map[string]string{
		"us-east-1":        "https://runtime.us-east-1.kiro.dev/v1/responses",
		"eu-central-1":     "https://runtime.eu-central-1.kiro.dev/v1/responses",
		"ap-south-1":       "https://runtime.us-east-1.kiro.dev/v1/responses",
		"evil.example/x#":  "https://runtime.us-east-1.kiro.dev/v1/responses",
		"us-east-1.evil.c": "https://runtime.us-east-1.kiro.dev/v1/responses",
		"":                 "https://runtime.us-east-1.kiro.dev/v1/responses",
	} {
		if got := createResponseURL(region); got != want {
			t.Errorf("%q: %s", region, got)
		}
	}
}

func TestCreateResponseHeadersMarkTheCredentialAndNeverSendAnIDEMachineSeedUnhashed(t *testing.T) {
	h := createResponseHeaders(kiroAuth{method: "api_key", token: "KEY"}, "arn:p")
	if h["Authorization"] != "Bearer KEY" || h["TokenType"] != "API_KEY" || h["x-amzn-kiro-profile"] != "arn:p" || h["x-amzn-kiro-attempt"] != "1;max=3" {
		t.Errorf("%v", h)
	}
	if h["x-amz-sso-bearer"] != "" {
		t.Error("an API key is not an SSO bearer")
	}
	h = createResponseHeaders(kiroAuth{method: "builder-id", token: "AT"}, "")
	if h["x-amz-sso-bearer"] != "AT" || h["TokenType"] != "" || h["x-amzn-kiro-profile"] != "" {
		t.Errorf("%v", h)
	}
	if h["User-Agent"] != upstream.KiroACPAgent || !strings.Contains(h["User-Agent"], upstream.KiroIDEVersion) {
		t.Errorf("user agent %q", h["User-Agent"])
	}
	if got := createResponseHeaders(kiroAuth{method: "external_idp", token: "X"}, "")["TokenType"]; got != "EXTERNAL_IDP" {
		t.Errorf("token type %q", got)
	}
}

// adapted runs a Responses event stream through the adapter and then through the
// Kiro codec the pipeline uses, so a drift in either breaks here.
func adapted(t *testing.T, events string) (text, reasoning string, calls []translate.ToolCallDelta, finish string, usage translate.Usage, errs int) {
	t.Helper()
	body, err := io.ReadAll(adaptResponses(io.NopCloser(strings.NewReader(events))))
	if err != nil {
		t.Fatal(err)
	}
	src := stream.NewReader(bytes.NewReader(body))
	rd := kiro.Codec{}.NewStreamReader(translate.NewCtx(translate.FormatKiro, translate.FormatOpenAI))
	collect := func(chunks []translate.Chunk) {
		for _, c := range chunks {
			text += c.Text
			reasoning += c.Reasoning
			calls = append(calls, c.ToolCalls...)
			if c.FinishReason != "" {
				finish = c.FinishReason
			}
			if c.Usage != nil {
				usage = translate.MergeUsage(usage, *c.Usage)
			}
			if c.Err != nil {
				errs++
			}
		}
	}
	for {
		ev, err := src.Next()
		if err != nil {
			break
		}
		collect(rd.Read(ev.Name, ev.Data))
	}
	collect(rd.Flush())
	return
}

func sse(events ...string) string {
	var b strings.Builder
	for _, e := range events {
		b.WriteString("data: " + e + "\n\n")
	}
	return b.String()
}

func TestAnAdaptedResponsesStreamReadsBackAsTextReasoningAndUsage(t *testing.T) {
	text, reasoning, calls, finish, usage, errs := adapted(t, sse(
		`{"type":"response.reasoning_text.delta","delta":"hm"}`,
		`{"type":"response.output_text.delta","delta":"Hel"}`,
		`{"type":"response.output_text.delta","delta":"lo"}`,
		`{"type":"response.refusal.delta","delta":"!"}`,
		`{"type":"response.completed","response":{"usage":{"input_tokens":12,"output_tokens":3}}}`,
	)+"data: [DONE]\n\n")
	if text != "Hello!" || reasoning != "hm" || len(calls) != 0 || errs != 0 {
		t.Errorf("text %q reasoning %q calls %v errs %d", text, reasoning, calls, errs)
	}
	if finish != translate.FinishStop || usage.Prompt != 12 || usage.Completion != 3 {
		t.Errorf("finish %q usage %+v", finish, usage)
	}
}

func TestAnAdaptedResponsesStreamReadsBackToolCalls(t *testing.T) {
	_, _, calls, finish, _, _ := adapted(t, sse(
		`{"type":"response.output_item.added","item":{"type":"function_call","call_id":"c1","name":"weather"}}`,
		`{"type":"response.function_call_arguments.delta","delta":"{\"city\":"}`,
		`{"type":"response.function_call_arguments.delta","delta":"\"Paris\"}"}`,
		`{"type":"response.output_item.done","item":{"type":"function_call","call_id":"c1","arguments":"{\"city\":\"Paris\"}"}}`,
		`{"type":"response.output_item.added","item":{"type":"function_call","call_id":"c2","name":"time"}}`,
		`{"type":"response.output_item.done","item":{"type":"function_call","call_id":"c2","arguments":"{}"}}`,
		`{"type":"response.completed","response":{}}`,
	))
	if len(calls) != 2 || calls[0].ID != "c1" || calls[0].Name != "weather" || calls[0].Arguments != `{"city":"Paris"}` ||
		calls[1].ID != "c2" || calls[1].Name != "time" || calls[1].Arguments != "{}" {
		t.Fatalf("%+v", calls)
	}
	if finish != translate.FinishToolCalls {
		t.Errorf("finish %q", finish)
	}
}

func TestAnIncompleteResponseEndsAsLengthAndAFailureIsAnError(t *testing.T) {
	_, _, _, finish, _, _ := adapted(t, sse(`{"type":"response.output_text.delta","delta":"x"}`, `{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`))
	if finish != translate.FinishLength {
		t.Errorf("finish %q", finish)
	}
	for _, ev := range []string{`{"type":"error","error":{"message":"boom"}}`, `{"type":"response.failed","response":{"error":{"message":"boom"}}}`} {
		if _, _, _, _, _, errs := adapted(t, sse(ev)); errs != 1 {
			t.Errorf("%s: %d errors", ev, errs)
		}
	}
}

func TestTheAdapterSkipsWhatItCannotReadAndStopsAtItsBound(t *testing.T) {
	text, _, _, _, _, errs := adapted(t, "event: x\n: comment\ndata: not json\n\n"+sse(`{"type":"response.output_text.delta","delta":"ok"}`, `[1]`, `"x"`, `{"type":7}`))
	if text != "ok" || errs != 0 {
		t.Errorf("text %q errs %d", text, errs)
	}
	long := "data: " + strings.Repeat("x", stream.MaxEventBytes+10) + "\n\n" + sse(`{"type":"response.output_text.delta","delta":"after"}`)
	body, err := io.ReadAll(adaptResponses(io.NopCloser(strings.NewReader(long))))
	if err != nil {
		t.Fatalf("an oversized event must end the stream, not fail the read: %v", err)
	}
	if strings.Contains(string(body), "after") || !strings.Contains(string(body), "error") {
		t.Errorf("an event past the bound must end the turn as an error, not be skipped: %s", body)
	}
}

func TestClosingTheAdapterClosesTheUpstreamBody(t *testing.T) {
	c := &closeSpy{Reader: strings.NewReader(sse(`{"type":"response.output_text.delta","delta":"x"}`))}
	rc := adaptResponses(c)
	_, _ = rc.Read(make([]byte, 1))
	if err := rc.Close(); err != nil || !c.closed {
		t.Errorf("closed %v, err %v", c.closed, err)
	}
}

type closeSpy struct {
	io.Reader
	closed bool
}

func (c *closeSpy) Close() error { c.closed = true; return nil }

func crKiro(t *testing.T, mode settings.KiroCreateResponse, replies ...tests.Reply) (*Kiro, *tests.Upstream, executor.Request) {
	t.Helper()
	up := tests.NewUpstream(t, replies...)
	d := &executor.Default{Client: kiroClient(t, up), Sleep: func(time.Duration) <-chan time.Time {
		ch := make(chan time.Time, 1)
		ch <- time.Now()
		return ch
	}}
	req := kiroRequest(t, up, crBody)
	// The shipped entry names its executor, which keeps the default one from
	// rotating to another address on a 429.
	req.Entry.Executor = upstream.ExecutorKiro
	return &Kiro{Default: d, CreateResponse: mode}, up, req
}

func TestA429RateExceededFallsBackToCreateResponse(t *testing.T) {
	k, up, req := crKiro(t, settings.KiroAuto,
		tests.Reply{Status: 429, Body: rateExceededBody},
		tests.Reply{ContentType: "text/event-stream", Body: sse(`{"type":"response.output_text.delta","delta":"hi"}`, `{"type":"response.completed","response":{}}`)},
	)
	resp, err := k.Execute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	calls := up.Calls()
	if resp.Status != 200 || len(calls) != 2 || calls[1].Path != "/v1/responses" {
		t.Fatalf("%d after %d calls (%v): %s", resp.Status, len(calls), calls, body)
	}
	if !strings.Contains(string(body), "assistantResponseEvent") || !strings.Contains(string(body), "hi") {
		t.Errorf("the answer is not in the events the pipeline reads: %s", body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("content type %q", ct)
	}
	if gjson.GetBytes(calls[1].Body, "model").String() != "claude-sonnet-4.5" {
		t.Errorf("request body: %s", calls[1].Body)
	}
	if calls[1].Header.Get("Authorization") != "Bearer TOKEN" {
		t.Errorf("headers: %v", calls[1].Header)
	}
}

func TestAnotherKindOf429DoesNotFallBack(t *testing.T) {
	for name, body := range map[string]string{"plain": `{"message":"slow down"}`, "empty": ``, "other reason": `{"reason":"MONTHLY_REQUEST_COUNT"}`} {
		k, up, req := crKiro(t, settings.KiroAuto, tests.Reply{Status: 429, Body: body})
		resp, err := k.Execute(context.Background(), req)
		if err != nil || resp.Status != 429 || len(up.Calls()) != 1 {
			t.Errorf("%s: %d after %d calls (%v)", name, resp.Status, len(up.Calls()), err)
		}
		raw, _ := io.ReadAll(resp.Body)
		if string(raw) != body {
			t.Errorf("%s: the refusal body was not passed on intact: %q", name, raw)
		}
	}
}

func TestOffNeverFallsBackAndAlwaysSkipsTheFirstTransport(t *testing.T) {
	k, up, req := crKiro(t, settings.KiroOff, tests.Reply{Status: 429, Body: rateExceededBody})
	if resp, _ := k.Execute(context.Background(), req); resp.Status != 429 || len(up.Calls()) != 1 || up.Calls()[0].Path == "/v1/responses" {
		t.Errorf("off: %d after %d calls", resp.Status, len(up.Calls()))
	}
	k, up, req = crKiro(t, settings.KiroAlways, tests.Reply{ContentType: "text/event-stream", Body: sse(`{"type":"response.completed","response":{}}`)})
	resp, err := k.Execute(context.Background(), req)
	if err != nil || resp.Status != 200 || len(up.Calls()) != 1 || up.Calls()[0].Path != "/v1/responses" {
		t.Errorf("always: %v %d after %d calls", err, resp.Status, len(up.Calls()))
	}
}

func TestAnAccountCanTurnTheFallbackOff(t *testing.T) {
	k, up, req := crKiro(t, settings.KiroAuto, tests.Reply{Status: 429, Body: rateExceededBody})
	req.Account = tests.Account(map[string]string{"authMethod": "builder-id", "kiroCreateResponse": "off"})
	if resp, _ := k.Execute(context.Background(), req); resp.Status != 429 || len(up.Calls()) != 1 {
		t.Errorf("%d after %d calls", resp.Status, len(up.Calls()))
	}
}

func TestNothingFallsBackForARequestThatIsNotAKiroConversation(t *testing.T) {
	k, up, req := crKiro(t, settings.KiroAuto, tests.Reply{Status: 429, Body: rateExceededBody})
	req.Body = []byte(`{"hello":1}`)
	if resp, _ := k.Execute(context.Background(), req); resp.Status != 429 || len(up.Calls()) != 1 {
		t.Errorf("%d after %d calls", resp.Status, len(up.Calls()))
	}
}

func TestCreateResponseRetriesOnceAfterItsOwnRateLimit(t *testing.T) {
	k, up, req := crKiro(t, settings.KiroAlways,
		tests.Reply{Status: 429, Body: `{}`},
		tests.Reply{ContentType: "text/event-stream", Body: sse(`{"type":"response.completed","response":{}}`)},
	)
	resp, err := k.Execute(context.Background(), req)
	if err != nil || resp.Status != 200 || len(up.Calls()) != 2 {
		t.Fatalf("%v %d after %d calls", err, resp.Status, len(up.Calls()))
	}
	k, up, req = crKiro(t, settings.KiroAlways, tests.Reply{Status: 429, Body: `{}`})
	if resp, _ := k.Execute(context.Background(), req); resp.Status != 429 || len(up.Calls()) != 2 {
		t.Errorf("a second 429 must be the answer after one retry: %d after %d calls", resp.Status, len(up.Calls()))
	}
}

func TestCreateResponseDoesNotRetryOnceTheCallerIsGone(t *testing.T) {
	k, up, req := crKiro(t, settings.KiroAlways, tests.Reply{Status: 429, Body: `{}`})
	ctx, cancel := context.WithCancel(context.Background())
	k.Default.Sleep = func(time.Duration) <-chan time.Time { cancel(); return make(chan time.Time) }
	if _, err := k.Execute(ctx, req); err == nil {
		t.Error("a cancelled wait must end the call with an error")
	}
	if len(up.Calls()) != 1 {
		t.Errorf("%d calls after cancellation", len(up.Calls()))
	}
}

func TestACreateResponseFailureIsReturnedAsTheAnswer(t *testing.T) {
	k, up, req := crKiro(t, settings.KiroAuto,
		tests.Reply{Status: 429, Body: rateExceededBody},
		tests.Reply{Status: 500, Body: `{"message":"down"}`},
	)
	resp, err := k.Execute(context.Background(), req)
	if err != nil || resp.Status != 500 || len(up.Calls()) != 2 {
		t.Fatalf("%v %d after %d calls", err, resp.Status, len(up.Calls()))
	}
	if raw, _ := io.ReadAll(resp.Body); !strings.Contains(string(raw), "down") {
		t.Errorf("the second transport's refusal was lost: %q", raw)
	}
}

func TestAJSONRequestIsNotSentToTheStreamingTransportOnly(t *testing.T) {
	k, up, req := crKiro(t, settings.KiroAuto,
		tests.Reply{Status: 429, Body: rateExceededBody},
		tests.Reply{ContentType: "text/event-stream", Body: sse(`{"type":"response.completed","response":{}}`)},
	)
	req.Stream = false
	resp, err := k.Execute(context.Background(), req)
	if err != nil || resp.Status != 200 || len(up.Calls()) != 2 {
		t.Fatalf("%v %d after %d calls", err, resp.Status, len(up.Calls()))
	}
}

func FuzzAdaptResponses(f *testing.F) {
	for _, s := range []string{"", "data: {}\n\n", sse(`{"type":"response.output_text.delta","delta":"x"}`), "data: [DONE]", "\x00\xff", "data: " + strings.Repeat("{", 5000)} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		_, _ = io.ReadAll(adaptResponses(io.NopCloser(strings.NewReader(s))))
	})
}

var _ = http.StatusOK
