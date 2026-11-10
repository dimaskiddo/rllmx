package stream

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"rllmx/app/llm/translate"
)

// fakeClient records frames; failAfter makes the nth write fail.
type fakeClient struct {
	mu        sync.Mutex
	frames    []translate.Frame
	raw       [][]byte
	failAfter int
}

func (f *fakeClient) Frame(fr translate.Frame) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failAfter > 0 && len(f.frames) >= f.failAfter {
		return errors.New("gone")
	}
	f.frames = append(f.frames, fr)
	return nil
}

func (f *fakeClient) Raw(b []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.raw = append(f.raw, b)
	return nil
}

func (f *fakeClient) text() string {
	var b strings.Builder
	for _, fr := range f.frames {
		b.Write(fr.Data)
		b.WriteByte('\n')
	}
	return b.String()
}

func (f *fakeClient) dones() int {
	n := 0
	for _, fr := range f.frames {
		if fr.Done {
			n++
		}
	}
	return n
}

// slowBody delivers its chunks with a pause before each, and closes on demand.
type slowBody struct {
	r      *io.PipeReader
	closed chan struct{}
	once   sync.Once
}

func newSlowBody() (*slowBody, *io.PipeWriter) {
	pr, pw := io.Pipe()
	return &slowBody{r: pr, closed: make(chan struct{})}, pw
}

func (b *slowBody) Read(p []byte) (int, error) { return b.r.Read(p) }
func (b *slowBody) Close() error {
	b.once.Do(func() { close(b.closed) })
	return b.r.CloseWithError(io.ErrClosedPipe)
}

func sseBody(s string) io.ReadCloser { return io.NopCloser(strings.NewReader(s)) }

const openAIStream = "data: {\"id\":\"chatcmpl-abc12345\",\"model\":\"m\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"Hi\"}}]}\n\n" +
	"data: {\"choices\":[{\"delta\":{\"content\":\" there\"},\"finish_reason\":\"stop\"}]}\n\n" +
	"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":3}}\n\n" +
	"data: [DONE]\n\n"

func job(body io.ReadCloser, up, down string, c Client) Job {
	return Job{
		Body: body, Source: NewReader(body), UpstreamFormat: up, ClientFormat: down,
		Ctx: translate.NewCtx(down, up), Client: c,
		Timers: Timers{FirstChunk: time.Second, Stall: time.Second, UsageWait: 100 * time.Millisecond},
	}
}

func TestTranslateOpenAIToClaude(t *testing.T) {
	c := &fakeClient{}
	res := Run(context.Background(), job(sseBody(openAIStream), "openai", "claude", c))
	if res.Err != nil || !res.Wrote || res.Usage.Prompt != 10 || res.Usage.Completion != 3 || res.Finish != "stop" {
		t.Fatalf("%+v", res)
	}
	if res.TTFT <= 0 || res.ContentLen != 8 {
		t.Fatalf("ttft %v len %d", res.TTFT, res.ContentLen)
	}
	got := c.text()
	for _, want := range []string{`"type":"message_start"`, `"text_delta"`, `"stop_reason":"end_turn"`, `"input_tokens":10`, `"type":"message_stop"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %s in\n%s", want, got)
		}
	}
	if c.dones() != 0 {
		t.Fatal("a Claude client got a [DONE]")
	}
}

func TestATranslatedStreamPastItsCapEndsWithAnErrorFrame(t *testing.T) {
	var in strings.Builder
	for range 10 {
		in.WriteString("data: {\"id\":\"chatcmpl-abc12345\",\"choices\":[{\"delta\":{\"content\":\"0123456789\"}}]}\n\n")
	}
	for _, client := range []string{"claude", "openai-responses"} {
		c := &fakeClient{}
		j := job(sseBody(in.String()), "openai", client, c)
		j.MaxBytes = 35
		res := Run(context.Background(), j)
		if !errors.Is(res.Err, ErrCollectedTooLarge) || res.Status != 502 {
			t.Fatalf("%s: err %v status %d", client, res.Err, res.Status)
		}
		if len(c.raw) != 1 || !strings.Contains(string(c.raw[0]), "too large") {
			t.Fatalf("%s: raw frames %q", client, c.raw)
		}
	}
}

func TestATranslatedStreamAtItsCapFinishesNormally(t *testing.T) {
	body := sseBody("data: {\"id\":\"chatcmpl-abc12345\",\"choices\":[{\"delta\":{\"content\":\"0123456789\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	j := job(body, "openai", "claude", &fakeClient{})
	j.MaxBytes = 10
	if res := Run(context.Background(), j); res.Err != nil {
		t.Fatalf("an answer exactly at the cap failed: %v", res.Err)
	}
}

func TestTranslateEndsWithExactlyOneDone(t *testing.T) {
	c := &fakeClient{}
	Run(context.Background(), job(sseBody(strings.ReplaceAll(openAIStream, "data: [DONE]\n\n", "")), "openai", "openai", c))
	// same-format passthrough: upstream sent no [DONE], the client still gets one.
	if c.dones() != 1 {
		t.Fatalf("%d done markers", c.dones())
	}
	c2 := &fakeClient{}
	Run(context.Background(), job(sseBody(openAIStream), "openai", "openai", c2))
	if c2.dones() != 1 {
		t.Fatalf("%d done markers with an upstream [DONE]", c2.dones())
	}
}

func TestAHeldFinishIsReleasedWhenTheUsageNeverComes(t *testing.T) {
	body, pw := newSlowBody()
	c := &fakeClient{}
	j := job(body, "openai", "claude", c)
	j.Source = NewReader(body)
	go func() {
		_, _ = pw.Write([]byte("data: {\"id\":\"chatcmpl-abc12345\",\"model\":\"m\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"x\"},\"finish_reason\":\"stop\"}]}\n\n"))
	}()
	start := time.Now()
	res := Run(context.Background(), j)
	if time.Since(start) > 900*time.Millisecond {
		t.Fatal("waited for the stall instead of the usage wait")
	}
	if res.Err != nil || !strings.Contains(c.text(), `"type":"message_stop"`) {
		t.Fatalf("%+v\n%s", res, c.text())
	}
}

func TestFirstChunkTimeoutIs504AndFramesTheClientError(t *testing.T) {
	body, _ := newSlowBody()
	c := &fakeClient{}
	j := job(body, "openai", "claude", c)
	j.Timers = Timers{FirstChunk: 40 * time.Millisecond, Stall: time.Second}
	res := Run(context.Background(), j)
	if !errors.Is(res.Err, ErrFirstChunk) || res.Status != 504 || res.Wrote && len(c.frames) > 0 {
		t.Fatalf("%+v", res)
	}
	if len(c.raw) != 1 || !bytes.HasPrefix(c.raw[0], []byte("event: error\n")) {
		t.Fatalf("%q", c.raw)
	}
	select {
	case <-body.closed:
	default:
		t.Fatal("the upstream was left open")
	}
}

func TestStallRestartsOnEveryEvent(t *testing.T) {
	body, pw := newSlowBody()
	c := &fakeClient{}
	j := job(body, "openai", "openai", c)
	j.Timers = Timers{FirstChunk: time.Second, Stall: 120 * time.Millisecond}
	go func() {
		for range 4 {
			_, _ = pw.Write([]byte("data: {\"id\":\"chatcmpl-abc12345\",\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n"))
			time.Sleep(60 * time.Millisecond)
		}
	}()
	res := Run(context.Background(), j)
	if !errors.Is(res.Err, ErrStall) || res.Status != 504 || len(c.frames) != 4 {
		t.Fatalf("%+v frames %d", res, len(c.frames))
	}
}

func TestABrokenStreamIs502NotA504(t *testing.T) {
	pr, pw := io.Pipe()
	c := &fakeClient{}
	body := io.NopCloser(pr)
	j := job(body, "openai", "openai", c)
	go func() {
		_, _ = pw.Write([]byte("data: {\"id\":\"chatcmpl-abc12345\",\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n"))
		_ = pw.CloseWithError(errors.New("reset"))
	}()
	res := Run(context.Background(), j)
	if res.Status != 502 || res.Err == nil {
		t.Fatalf("%+v", res)
	}
	if len(c.raw) != 1 || !bytes.Contains(c.raw[0], []byte("[DONE]")) {
		t.Fatalf("an OpenAI client needs the error frame then [DONE]: %q", c.raw)
	}
}

func TestAClientThatLeavesStopsTheUpstream(t *testing.T) {
	body, pw := newSlowBody()
	c := &fakeClient{failAfter: 1}
	j := job(body, "openai", "claude", c)
	go func() {
		for range 20 {
			if _, err := pw.Write([]byte("data: {\"id\":\"chatcmpl-abc12345\",\"model\":\"m\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"a\"}}]}\n\n")); err != nil {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	res := Run(context.Background(), j)
	if !errors.Is(res.Err, ErrClientGone) {
		t.Fatalf("%+v", res)
	}
	select {
	case <-body.closed:
	case <-time.After(time.Second):
		t.Fatal("the upstream kept running")
	}
	if len(c.raw) != 0 {
		t.Fatal("an error frame was written to a client that left")
	}
}

func TestContextCancelStopsTheStream(t *testing.T) {
	body, _ := newSlowBody()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(30 * time.Millisecond); cancel() }()
	j := job(body, "openai", "openai", &fakeClient{})
	j.Timers = Timers{FirstChunk: 5 * time.Second}
	c := j.Client.(*fakeClient)
	res := Run(ctx, j)
	if !errors.Is(res.Err, ErrClientGone) {
		t.Fatalf("%+v", res)
	}
	if len(c.raw) != 0 {
		t.Fatalf("a client that left got a frame: %q", c.raw)
	}
}

func TestAWriteErrorStopsAtTheFirstFailedFrame(t *testing.T) {
	c := &fakeClient{failAfter: 2}
	body := sseBody(strings.Repeat("data: {\"id\":\"chatcmpl-abc12345\",\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n", 10))
	res := Run(context.Background(), job(body, "openai", "openai", c))
	if !errors.Is(res.Err, ErrClientGone) || len(c.frames) != 2 {
		t.Fatalf("%+v frames %d", res, len(c.frames))
	}
}

func TestAnUpstreamErrorEventFailsTheStreamInTheClientsShape(t *testing.T) {
	c := &fakeClient{}
	body := sseBody("event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"secret upstream text\"}}\n\n")
	res := Run(context.Background(), job(body, "claude", "openai-responses", c))
	if res.Status != 502 || res.Err == nil {
		t.Fatalf("%+v", res)
	}
	if len(c.raw) != 1 || !bytes.HasPrefix(c.raw[0], []byte("event: response.failed")) || bytes.Contains(c.raw[0], []byte("secret")) {
		t.Fatalf("%q", c.raw)
	}
}

func TestEstimateIsFlaggedWhenTheUpstreamReportsNothing(t *testing.T) {
	c := &fakeClient{}
	body := sseBody("data: {\"id\":\"chatcmpl-abc12345\",\"choices\":[{\"delta\":{\"content\":\"abcdefgh\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	j := job(body, "openai", "claude", c)
	j.RequestBytes = 400
	res := Run(context.Background(), j)
	if !res.Usage.Estimated || res.Usage.Prompt != 100 || res.Usage.Completion != 2 {
		t.Fatalf("%+v", res.Usage)
	}
	empty := Run(context.Background(), job(sseBody("data: [DONE]\n\n"), "openai", "claude", &fakeClient{}))
	if empty.Usage.Estimated || empty.Usage.Prompt != 0 {
		t.Fatalf("an empty stream was estimated: %+v", empty.Usage)
	}
}

func TestTheClientBufferNeverReachesTheRow(t *testing.T) {
	c := &fakeClient{}
	j := job(sseBody(openAIStream), "openai", "claude", c)
	j.Buffer = true
	res := Run(context.Background(), j)
	if res.Usage.Prompt != 10 || !strings.Contains(c.text(), `"input_tokens":2010`) {
		t.Fatalf("row %d\n%s", res.Usage.Prompt, c.text())
	}
}

func TestPassthroughRepairsOpenAIChunks(t *testing.T) {
	c := &fakeClient{}
	body := sseBody("data: {\"id\":\"chat\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"x\",\"tool_calls\":[]}}],\"prompt_filter_results\":[1]}\n\n" +
		"data: not json\n\n" +
		"data: {\"id\":\"chat\",\"choices\":[{\"delta\":{\"content\":\"\"}}]}\n\n" +
		"data: {\"id\":\"chat\",\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":2,\"function\":{\"name\":\"f\",\"arguments\":\"{}\"}}]}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":1}}\n\n")
	res := Run(context.Background(), job(body, "openai", "openai", c))
	if res.Err != nil || res.Usage.Prompt != 4 || res.Finish != "tool_calls" || c.dones() != 1 {
		t.Fatalf("%+v dones %d", res, c.dones())
	}
	got := c.text()
	if strings.Contains(got, "not json") || strings.Contains(got, "prompt_filter_results") || strings.Contains(got, `"tool_calls":[]`) {
		t.Fatalf("not repaired:\n%s", got)
	}
	if strings.Contains(got, `"id":"chat"`) || !strings.Contains(got, `"object":"chat.completion.chunk"`) || !strings.Contains(got, `"id":"call_stream_2"`) {
		t.Fatalf("ids or fields missing:\n%s", got)
	}
	if len(c.frames) != 4 {
		t.Fatalf("%d frames:\n%s", len(c.frames), got)
	}
}

func TestAnUpstreamDoneMarkerIsNeverForwardedAsData(t *testing.T) {
	c := &fakeClient{}
	Run(context.Background(), job(sseBody(openAIStream), "openai", "openai", c))
	for _, f := range c.frames {
		if !f.Done && strings.Contains(string(f.Data), "DONE") {
			t.Fatalf("the marker went out as data: %q", f.Data)
		}
	}
}

func TestPassthroughKeepsEventNamesAndFailsAnUnfinishedResponsesStream(t *testing.T) {
	c := &fakeClient{}
	body := sseBody("event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"r\"}}\n\n")
	res := Run(context.Background(), job(body, "openai-responses", "openai-responses", c))
	if len(c.frames) < 1 || c.frames[0].Event != "response.created" {
		t.Fatalf("%+v", c.frames)
	}
	if res.Err == nil || len(c.raw) != 1 || !bytes.HasPrefix(c.raw[0], []byte("event: response.failed")) || c.dones() != 1 {
		t.Fatalf("%+v raw %q dones %d", res, c.raw, c.dones())
	}
	ok := &fakeClient{}
	Run(context.Background(), job(sseBody("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":2,\"output_tokens\":1}}}\n\n"),
		"openai-responses", "openai-responses", ok))
	if len(ok.raw) != 0 || ok.dones() != 1 {
		t.Fatalf("a finished stream was failed: raw %q", ok.raw)
	}
}

func TestPassthroughOfGeminiAndClaudeWritesNoDone(t *testing.T) {
	for _, format := range []string{"gemini", "claude", "ollama"} {
		c := &fakeClient{}
		body := sseBody("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"a\"}]}}],\"type\":\"x\",\"done\":false}\n\n")
		Run(context.Background(), job(body, format, format, c))
		if c.dones() != 0 || len(c.frames) != 1 {
			t.Errorf("%s: %d frames %d dones", format, len(c.frames), c.dones())
		}
	}
}

func TestTrackerMergesAndEstimates(t *testing.T) {
	tr := NewTracker("claude")
	tr.Feed([]byte(`{"type":"message_start","message":{"usage":{"input_tokens":10,"cache_read_input_tokens":5,"output_tokens":1}}}`))
	tr.Feed([]byte(`{"type":"message_delta","usage":{"output_tokens":20}}`))
	u := tr.Final(0)
	if u.Prompt != 15 || u.Cached != 5 || u.Completion != 20 || u.Estimated {
		t.Fatalf("%+v", u)
	}
}

func TestAddClientBufferPerFormat(t *testing.T) {
	cases := []struct{ format, in, want string }{
		{"openai", `{"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`, `{"usage":{"prompt_tokens":2010,"completion_tokens":5,"total_tokens":2015}}`},
		{"openai", `{"usage":{"prompt_tokens":0,"completion_tokens":5,"total_tokens":5}}`, `{"usage":{"prompt_tokens":0,"completion_tokens":5,"total_tokens":5}}`},
		{"claude", `{"type":"message_start","message":{"usage":{"input_tokens":7}}}`, `{"type":"message_start","message":{"usage":{"input_tokens":2007}}}`},
		{"claude", `{"type":"message_delta","usage":{"input_tokens":7,"output_tokens":2}}`, `{"type":"message_delta","usage":{"input_tokens":2007,"output_tokens":2}}`},
		{"openai-responses", `{"response":{"usage":{"input_tokens":3,"total_tokens":4}}}`, `{"response":{"usage":{"input_tokens":2003,"total_tokens":2004}}}`},
		{"gemini", `{"usageMetadata":{"promptTokenCount":3,"totalTokenCount":9}}`, `{"usageMetadata":{"promptTokenCount":2003,"totalTokenCount":2009}}`},
		{"ollama", `{"done":true,"prompt_eval_count":6,"eval_count":1}`, `{"done":true,"prompt_eval_count":2006,"eval_count":1}`},
		{"ollama", `{"done":false,"prompt_eval_count":6}`, `{"done":false,"prompt_eval_count":6}`},
		{"openai", `{"choices":[]}`, `{"choices":[]}`},
		{"openai", `not json`, `not json`},
	}
	for _, c := range cases {
		if got := string(AddClientBuffer(c.format, []byte(c.in))); got != c.want {
			t.Errorf("%s %s\n got  %s\n want %s", c.format, c.in, got, c.want)
		}
	}
}

func TestCollectAddsAStreamUpIntoOneAnswer(t *testing.T) {
	body := sseBody("data: {\"id\":\"chatcmpl-abc12345\",\"model\":\"m\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"reasoning_content\":\"hmm \",\"content\":\"Hel\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"lo\",\"tool_calls\":[{\"index\":0,\"id\":\"c1\",\"function\":{\"name\":\"f\",\"arguments\":\"{\\\"a\\\"\"}}]}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\":1}\"}}]},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":4}}\n\n" +
		"data: [DONE]\n\n")
	got, err := Collect(context.Background(), body, NewReader(body), "openai", translate.NewCtx("openai", "openai"), Timers{FirstChunk: time.Second, Stall: time.Second}, 0)
	if err != nil {
		t.Fatal(err)
	}
	m := got.Response.Message
	if m.Text() != "Hello" || m.Reasoning != "hmm " || len(m.ToolCalls) != 1 || m.ToolCalls[0].Arguments != `{"a":1}` || m.ToolCalls[0].ID != "c1" {
		t.Fatalf("%+v", m)
	}
	if got.Finish != "tool_calls" || got.Usage.Prompt != 9 || got.Response.ID != "chatcmpl-abc12345" {
		t.Fatalf("%+v", got)
	}
	enc, err := (translate.ClaudeCodec{}).WriteResponse(&got.Response, translate.NewCtx("claude", "openai"))
	if err != nil || !strings.Contains(string(enc), `"tool_use"`) {
		t.Fatalf("%s %v", enc, err)
	}
}

func TestCollectStopsPastItsCap(t *testing.T) {
	frame := func(delta string) string {
		return "data: {\"id\":\"chatcmpl-abc12345\",\"choices\":[{\"delta\":" + delta + "}]}\n\n"
	}
	cases := map[string]string{
		"text": frame(`{"content":"0123456789"}`) + frame(`{"content":"0123456789"}`),
		"args": frame(`{"tool_calls":[{"index":0,"id":"c","function":{"name":"f","arguments":"0123456789"}}]}`) +
			frame(`{"tool_calls":[{"index":0,"function":{"arguments":"0123456789"}}]}`),
	}
	for name, in := range cases {
		body := sseBody(in)
		_, err := collectUpTo(context.Background(), body, NewReader(body), "openai", translate.NewCtx("", "openai"), Timers{}, 0, 15)
		if !errors.Is(err, ErrCollectedTooLarge) {
			t.Errorf("%s: err %v, want ErrCollectedTooLarge", name, err)
		}
		if got := ReasonText(err); got != "The upstream answer is too large" {
			t.Errorf("%s: reason %q", name, got)
		}
	}
	body := sseBody(cases["text"])
	if _, err := collectUpTo(context.Background(), body, NewReader(body), "openai", translate.NewCtx("", "openai"), Timers{}, 0, 20); err != nil {
		t.Fatalf("an answer exactly at the cap failed: %v", err)
	}
}

func TestCollectJoinsManySmallArgumentFragments(t *testing.T) {
	var in strings.Builder
	in.WriteString("data: {\"id\":\"chatcmpl-abc12345\",\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c\",\"function\":{\"name\":\"f\",\"arguments\":\"[\"}}]}}]}\n\n")
	for range 500 {
		in.WriteString("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"1,\"}}]}}]}\n\n")
	}
	in.WriteString("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"2]\"}}]}}]}\n\n")
	body := sseBody(in.String())
	got, err := Collect(context.Background(), body, NewReader(body), "openai", translate.NewCtx("", "openai"), Timers{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := "[" + strings.Repeat("1,", 500) + "2]"
	if a := got.Response.Message.ToolCalls[0].Arguments; a != want {
		t.Fatalf("arguments %d bytes, want %d", len(a), len(want))
	}
}

func TestCollectKeepsToolCallsInTheirOrder(t *testing.T) {
	body := sseBody("data: {\"id\":\"chatcmpl-abc12345\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":2,\"id\":\"c2\",\"function\":{\"name\":\"third\",\"arguments\":\"{}\"}}]}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c0\",\"function\":{\"name\":\"first\",\"arguments\":\"{}\"}}]}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":1,\"id\":\"c1\",\"function\":{\"name\":\"second\"}}]}}]}\n\n")
	got, err := Collect(context.Background(), body, NewReader(body), "openai", translate.NewCtx("", "openai"), Timers{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range got.Response.Message.ToolCalls {
		names = append(names, c.Name)
		if c.Arguments == "" {
			t.Fatalf("%s has no arguments", c.Name)
		}
	}
	if strings.Join(names, ",") != "first,second,third" {
		t.Fatalf("%v", names)
	}
}

func TestCollectFailsOnAnErrorEventAndOnATimeout(t *testing.T) {
	body := sseBody("data: {\"error\":{\"message\":\"x\"}}\n\n")
	if _, err := Collect(context.Background(), body, NewReader(body), "openai", translate.NewCtx("", "openai"), Timers{}, 0); !errors.Is(err, ErrStreamFailed) {
		t.Fatalf("err %v", err)
	}
	slow, _ := newSlowBody()
	if _, err := Collect(context.Background(), slow, NewReader(slow), "openai", translate.NewCtx("", "openai"), Timers{FirstChunk: 30 * time.Millisecond}, 0); !errors.Is(err, ErrFirstChunk) {
		t.Fatalf("err %v", err)
	}
}

// runWithin fails the test, rather than hanging it, when Run outlives limit.
func runWithin(t *testing.T, limit time.Duration, j Job) Result {
	t.Helper()
	done := make(chan Result, 1)
	go func() { done <- Run(context.Background(), j) }()
	select {
	case r := <-done:
		return r
	case <-time.After(limit):
		_ = j.Body.Close()
		t.Fatalf("Run did not return within %v", limit)
		return Result{}
	}
}

func TestASilentUpstreamAfterTheFirstEventIsCutByTheStallTimer(t *testing.T) {
	body, pw := newSlowBody()
	c := &fakeClient{}
	j := job(body, "openai", "openai", c)
	j.Timers = Timers{FirstChunk: time.Second, Stall: 60 * time.Millisecond}
	go func() {
		_, _ = pw.Write([]byte("data: {\"id\":\"chatcmpl-abc12345\",\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n"))
	}()
	res := runWithin(t, 800*time.Millisecond, j)
	if !errors.Is(res.Err, ErrStall) || res.Status != 504 {
		t.Fatalf("%+v", res)
	}
}

func TestTheFirstChunkWaitIsNotTheStallWait(t *testing.T) {
	body, pw := newSlowBody()
	j := job(body, "openai", "openai", &fakeClient{})
	j.Timers = Timers{FirstChunk: 400 * time.Millisecond, Stall: 40 * time.Millisecond}
	go func() {
		time.Sleep(120 * time.Millisecond)
		_, _ = pw.Write([]byte("data: {\"id\":\"chatcmpl-abc12345\",\"choices\":[{\"delta\":{\"content\":\"a\"},\"finish_reason\":\"stop\"}]}\n\n"))
		_ = pw.Close()
	}()
	if res := runWithin(t, 2*time.Second, j); res.Err != nil {
		t.Fatalf("a slow start was cut by the stall timer: %+v", res)
	}
}

func TestZeroTimersNeverFire(t *testing.T) {
	body, pw := newSlowBody()
	j := job(body, "openai", "openai", &fakeClient{})
	j.Timers = Timers{}
	go func() {
		time.Sleep(80 * time.Millisecond)
		_, _ = pw.Write([]byte("data: [DONE]\n\n"))
		_ = pw.Close()
	}()
	if res := runWithin(t, 2*time.Second, j); res.Err != nil {
		t.Fatalf("%+v", res)
	}
}

func TestKindOfAndPageTitle(t *testing.T) {
	for in, want := range map[string]Kind{
		"text/event-stream; charset=utf-8": KindSSE, "application/x-ndjson": KindNDJSON, "application/json": KindJSON,
		"application/problem+json": KindJSON, "text/html": KindOther, "": KindOther,
	} {
		if KindOf(in) != want {
			t.Errorf("%q: %v", in, KindOf(in))
		}
	}
	if got := PageTitle([]byte("<html><head><title>502 Bad <b>Gateway</b></title></head><body>" + strings.Repeat("x", 500) + "</body></html>")); got != "502 Bad Gateway" {
		t.Fatalf("%q", got)
	}
	if got := PageTitle([]byte("<html>" + strings.Repeat("x", 500) + "</html>")); got != "" {
		t.Fatalf("%q", got)
	}
	if got := PageTitle([]byte("&lt;script&gt;alert(1)&lt;/script&gt; down")); strings.ContainsAny(got, "<>") {
		t.Fatalf("markup survived: %q", got)
	}
}
