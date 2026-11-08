package translate

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// script is one assistant turn in the hub: reasoning, text, one tool call
// split over fragments, then a finish and a separate usage report.
func script() []Chunk {
	return []Chunk{
		{ID: "chatcmpl-abcdef12", Model: "m", Role: RoleAssistant, Reasoning: "think "},
		{Reasoning: "more"},
		{Text: "Hello "},
		{Text: "world"},
		{ToolCalls: []ToolCallDelta{{Index: 0, ID: "call_1", Name: "weather", Arguments: `{"city":`}}},
		{ToolCalls: []ToolCallDelta{{Index: 0, Arguments: `"Paris"}`}}},
		{FinishReason: FinishToolCalls},
		{Usage: &Usage{Prompt: 30, Completion: 12, Cached: 5}},
	}
}

// emit runs chunks through a format's writer and returns its frames.
func emit(t *testing.T, format string, chunks []Chunk) []Frame {
	t.Helper()
	c, _ := Lookup(format)
	w := c.NewStreamWriter(NewCtx(format, format))
	var out []Frame
	for _, ch := range chunks {
		out = append(out, w.Write(ch)...)
	}
	return append(out, w.Close()...)
}

// back reads a writer's frames with the same format's reader.
func back(t *testing.T, format string, frames []Frame) []Chunk {
	t.Helper()
	c, _ := Lookup(format)
	r := c.NewStreamReader(NewCtx(format, format))
	var out []Chunk
	for _, f := range frames {
		if f.Done {
			continue
		}
		out = append(out, r.Read(f.Event, f.Data)...)
	}
	return append(out, r.Flush()...)
}

// gist folds chunks into what every format must carry.
type gist struct {
	text, reasoning, name, args, id, finish string
	usage                                   Usage
	roles                                   int
}

func fold(chunks []Chunk) gist {
	var g gist
	for _, c := range chunks {
		g.text += c.Text
		g.reasoning += c.Reasoning
		if c.Role != "" {
			g.roles++
		}
		for _, tc := range c.ToolCalls {
			if tc.Name != "" {
				g.name = tc.Name
			}
			if tc.ID != "" {
				g.id = tc.ID
			}
			g.args += tc.Arguments
		}
		if c.FinishReason != "" {
			g.finish = c.FinishReason
		}
		if c.Usage != nil {
			g.usage = MergeUsage(g.usage, *c.Usage)
		}
	}
	return g
}

func TestEveryFormatSurvivesItsOwnRoundTrip(t *testing.T) {
	for _, format := range Formats() {
		t.Run(format, func(t *testing.T) {
			got := fold(back(t, format, emit(t, format, script())))
			if got.text != "Hello world" || got.name != "weather" || got.args != `{"city":"Paris"}` {
				t.Fatalf("%+v", got)
			}
			if got.finish != FinishToolCalls {
				t.Fatalf("finish %q", got.finish)
			}
			if got.usage.Prompt != 30 || got.usage.Completion != 12 {
				t.Fatalf("usage %+v", got.usage)
			}
			if format != FormatOllama && got.reasoning != "think more" {
				t.Fatalf("reasoning %q", got.reasoning)
			}
			if got.roles != 1 {
				t.Fatalf("%d role chunks", got.roles)
			}
		})
	}
}

func TestEveryPairOfFormatsKeepsTheTurn(t *testing.T) {
	for _, from := range Formats() {
		for _, to := range Formats() {
			t.Run(from+"_to_"+to, func(t *testing.T) {
				upstream := back(t, from, emit(t, from, script()))
				got := fold(back(t, to, emit(t, to, upstream)))
				if got.text != "Hello world" || got.name != "weather" || got.args != `{"city":"Paris"}` || got.finish != FinishToolCalls {
					t.Fatalf("%+v", got)
				}
				if got.usage.Prompt != 30 || got.usage.Completion != 12 {
					t.Fatalf("usage %+v", got.usage)
				}
			})
		}
	}
}

func TestEveryReaderHonoursTheHubInvariants(t *testing.T) {
	for _, format := range Formats() {
		chunks := back(t, format, emit(t, format, script()))
		if len(chunks) == 0 || chunks[0].Role != RoleAssistant {
			t.Errorf("%s: first chunk has no role", format)
		}
		finishes := 0
		for _, c := range chunks {
			if c.FinishReason != "" {
				finishes++
			}
		}
		if finishes != 1 {
			t.Errorf("%s: %d finish chunks", format, finishes)
		}
	}
}

func TestAFinishWaitsForItsUsageAndCloseReleasesIt(t *testing.T) {
	for _, format := range Formats() {
		c, _ := Lookup(format)
		w := c.NewStreamWriter(NewCtx(format, format))
		_ = w.Write(Chunk{ID: "chatcmpl-abcdef12", Model: "m", Role: RoleAssistant, Text: "hi"})
		f := w.Write(Chunk{FinishReason: FinishStop})
		if !w.Held() {
			t.Errorf("%s: finish not held", format)
		}
		for _, fr := range f {
			if bytes.Contains(fr.Data, []byte(`"finish_reason":"stop"`)) || fr.Event == "message_stop" || fr.Event == "response.completed" {
				t.Errorf("%s: closing sent before usage: %s", format, fr.Data)
			}
		}
		if len(w.Close()) == 0 {
			t.Errorf("%s: Close released nothing", format)
		}
		if w.Held() {
			t.Errorf("%s: still held after Close", format)
		}
		if len(w.Close()) != 0 {
			t.Errorf("%s: second Close wrote again", format)
		}
	}
}

func TestOnlyOpenAIEndsWithDone(t *testing.T) {
	for _, format := range Formats() {
		dones := 0
		for _, f := range emit(t, format, script()) {
			if f.Done {
				dones++
			}
		}
		want := 0
		if format == FormatOpenAI {
			want = 1
		}
		if dones != want {
			t.Errorf("%s: %d done markers, want %d", format, dones, want)
		}
	}
}

func TestOllamaWriterSendsOneDoneLine(t *testing.T) {
	n := 0
	for _, f := range emit(t, FormatOllama, script()) {
		if strings.Contains(string(f.Data), `"done":true`) {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d done lines", n)
	}
}

func TestUsageMergesByMaximumAcrossEvents(t *testing.T) {
	c, _ := Lookup(FormatClaude)
	r := c.NewStreamReader(NewCtx("", FormatClaude))
	var all []Chunk
	for _, ev := range []string{
		`{"type":"message_start","message":{"id":"m1","model":"x","usage":{"input_tokens":10,"cache_read_input_tokens":4,"cache_creation_input_tokens":2,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":9}}`,
	} {
		ch := r.Read("", []byte(ev))
		all = append(all, ch...)
	}
	u := fold(all).usage
	if u.Prompt != 16 || u.Cached != 4 || u.CacheWrite != 2 || u.Completion != 9 {
		t.Fatalf("%+v", u)
	}
}

func TestEveryUpstreamErrorBecomesAnErrorChunk(t *testing.T) {
	cases := map[string]string{
		FormatOpenAI:    `{"error":{"type":"x","message":"boom"}}`,
		FormatClaude:    `{"type":"error","error":{"type":"overloaded_error","message":"boom"}}`,
		FormatResponses: `{"type":"response.failed","response":{"error":{"message":"boom"}}}`,
		FormatGemini:    `{"error":{"status":"UNAVAILABLE","message":"boom"}}`,
		FormatOllama:    `{"error":"boom"}`,
	}
	for format, ev := range cases {
		c, _ := Lookup(format)
		ch := c.NewStreamReader(NewCtx("", format)).Read("", []byte(ev))
		if len(ch) != 1 || ch[0].Err == nil || ch[0].Err.Message != "boom" {
			t.Errorf("%s: %+v", format, ch)
		}
	}
}

func TestAToolFragmentOpeningAnIndexWithoutAnIDGetsOne(t *testing.T) {
	c, _ := Lookup(FormatOpenAI)
	r := c.NewStreamReader(NewCtx("", FormatOpenAI))
	ch := r.Read("", []byte(`{"id":"x","choices":[{"delta":{"tool_calls":[{"index":1,"function":{"name":"f","arguments":"{"}}]}}]}`))
	if len(ch) != 1 || ch[0].ToolCalls[0].ID == "" {
		t.Fatalf("%+v", ch)
	}
	again := r.Read("", []byte(`{"id":"x","choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":"}"}}]}}]}`))
	if again[0].ToolCalls[0].ID != "" {
		t.Fatal("a later fragment repeated the id")
	}
	empty := r.Read("", []byte(`{"id":"x","choices":[{"delta":{"content":"","tool_calls":[]}}]}`))
	if len(empty) != 0 {
		t.Fatalf("an empty delta passed: %+v", empty)
	}
}

func TestARepeatedToolIDOpensNoSecondCall(t *testing.T) {
	c, _ := Lookup(FormatOpenAI)
	r := c.NewStreamReader(NewCtx("", FormatOpenAI))
	first := r.Read("", []byte(`{"id":"x","choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"f","arguments":"{"}}]}}]}`))
	again := r.Read("", []byte(`{"id":"x","choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"arguments":"}"}}]}}]}`))
	if first[0].ToolCalls[0].ID != "c1" || again[0].ToolCalls[0].ID != "" {
		t.Fatalf("%+v %+v", first, again)
	}
	var out []Frame
	w := (ClaudeCodec{}).NewStreamWriter(NewCtx("", FormatClaude))
	for _, ch := range []Chunk{
		{ID: "m", Model: "m", Role: RoleAssistant, ToolCalls: []ToolCallDelta{{Index: 0, ID: "c1", Name: "f", Arguments: "{"}}},
		{ToolCalls: []ToolCallDelta{{Index: 0, ID: "c1", Arguments: "}"}}},
		{FinishReason: FinishToolCalls, Usage: &Usage{Prompt: 1, Completion: 1}},
	} {
		f := w.Write(ch)
		out = append(out, f...)
	}
	starts := 0
	for _, f := range out {
		if f.Event == "content_block_start" {
			starts++
		}
	}
	if starts != 1 {
		t.Fatalf("%d tool blocks opened for one call", starts)
	}
}

func TestTheOpenAIWriterOpensACallOnceWithItsIDAndName(t *testing.T) {
	var out []Frame
	w := (OpenAICodec{}).NewStreamWriter(NewCtx("", FormatOpenAI))
	for _, ch := range []Chunk{
		{ID: "chatcmpl-abcdef12", Model: "m", Role: RoleAssistant, ToolCalls: []ToolCallDelta{{Index: 0, ID: "c1", Name: "f", Arguments: "{"}}},
		{ToolCalls: []ToolCallDelta{{Index: 0, ID: "c1", Name: "f", Arguments: "}"}}},
	} {
		f := w.Write(ch)
		out = append(out, f...)
	}
	if len(out) != 2 || strings.Count(string(out[0].Data), `"id":"c1"`) != 1 || strings.Contains(string(out[1].Data), `"id":"c1"`) || strings.Contains(string(out[1].Data), `"name"`) {
		t.Fatalf("%s | %s", out[0].Data, out[1].Data)
	}
}

func TestTheOpenAIWriterReplacesAShortID(t *testing.T) {
	frames := emit(t, FormatOpenAI, []Chunk{{ID: "chat", Model: "m", Role: RoleAssistant, Text: "a"}, {Text: "b"}})
	var ids []string
	for _, f := range frames {
		if f.Done {
			continue
		}
		var o struct{ ID, Object string }
		_ = json.Unmarshal(f.Data, &o)
		ids = append(ids, o.ID)
		if o.Object != "chat.completion.chunk" {
			t.Fatalf("object %q", o.Object)
		}
	}
	if len(ids) < 2 || ids[0] == "chat" || ids[0] != ids[1] {
		t.Fatalf("%v", ids)
	}
}

func TestResponsesAcceptsDoneAndCompletedOnce(t *testing.T) {
	c, _ := Lookup(FormatResponses)
	r := c.NewStreamReader(NewCtx("", FormatResponses))
	_ = r.Read("", []byte(`{"type":"response.created","response":{"id":"r"}}`))
	a := r.Read("", []byte(`{"type":"response.done","response":{"usage":{"input_tokens":3,"output_tokens":4}}}`))
	b := r.Read("", []byte(`{"type":"response.completed","response":{}}`))
	if len(a) != 1 || a[0].FinishReason == "" || a[0].Usage.Prompt != 3 || len(b) != 0 {
		t.Fatalf("%+v %+v", a, b)
	}
}

func TestTheResponsesWriterNamesCustomToolCalls(t *testing.T) {
	ctx := NewCtx("", FormatResponses)
	ctx.CustomTools["patch"] = true
	w := (ResponsesCodec{}).NewStreamWriter(ctx)
	var out []Frame
	for _, ch := range []Chunk{
		{ID: "r1", Model: "m", Role: RoleAssistant, ToolCalls: []ToolCallDelta{{Index: 0, ID: "c1", Name: "patch", Arguments: `{"input":"diff"}`}}},
		{FinishReason: FinishToolCalls, Usage: &Usage{Prompt: 1, Completion: 1}},
	} {
		f := w.Write(ch)
		out = append(out, f...)
	}
	joined := ""
	seqs := 0
	for _, f := range out {
		joined += string(f.Data)
		if strings.Contains(string(f.Data), `"sequence_number"`) {
			seqs++
		}
	}
	if !strings.Contains(joined, `"custom_tool_call"`) || strings.Contains(joined, `"function_call"`) || seqs != len(out) {
		t.Fatalf("%s", joined)
	}
}

func TestTheGeminiWriterHoldsACallUntilTheTurnEnds(t *testing.T) {
	w := (GeminiCodec{}).NewStreamWriter(NewCtx("", FormatGemini))
	f1 := w.Write(Chunk{ID: "r", Model: "m", Role: RoleAssistant, ToolCalls: []ToolCallDelta{{Index: 0, ID: "c", Name: "f", Arguments: `{"a":`}}})
	f2 := w.Write(Chunk{ToolCalls: []ToolCallDelta{{Index: 0, Arguments: `1}`}}})
	if len(f1)+len(f2) != 0 {
		t.Fatal("a call left before the turn ended")
	}
	f3 := w.Write(Chunk{FinishReason: FinishToolCalls, Usage: &Usage{Prompt: 2, Completion: 3}})
	if len(f3) != 2 || !bytes.Contains(f3[0].Data, []byte(`"args":{"a":1}`)) {
		t.Fatalf("%d frames: %s", len(f3), f3[0].Data)
	}
}

func TestThinkTagsSplitAcrossPieces(t *testing.T) {
	var s thinkSplitter
	var segs []segment
	for _, in := range []string{"pre <thi", "nk>idea</th", "ink> post", " tail <"} {
		segs = append(segs, s.split(in)...)
	}
	segs = append(segs, s.drain()...)
	var text, reason string
	for _, sg := range segs {
		if sg.reasoning {
			reason += sg.text
		} else {
			text += sg.text
		}
	}
	if text != "pre  post tail <" || reason != "idea" {
		t.Fatalf("text %q reason %q", text, reason)
	}
}

func TestReadToolArgumentsAreRepaired(t *testing.T) {
	cases := map[string]string{
		`{"file_path":"/a","limit":"50","offset":"-3"}`: `{"file_path":"/a","limit":50,"offset":0}`,
		`{"file_path":"/a","limit":99999}`:              `{"file_path":"/a","limit":2000}`,
		`{"file_path":"/a","limit":0}`:                  `{"file_path":"/a"}`,
		`{"file_path":"/a.pdf","pages":"1-3"}`:          `{"file_path":"/a.pdf","pages":"1-3"}`,
		`{"file_path":"/a.txt","pages":"1-3"}`:          `{"file_path":"/a.txt"}`,
		`{"file_path":"/a.pdf","pages":"all"}`:          `{"file_path":"/a.pdf"}`,
		``:                                              `{}`,
		`not json`:                                      `not json`,
	}
	for in, want := range cases {
		if got := repairToolArgs("Read", in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
	if got := repairToolArgs("Bash", `{"limit":"5"}`); got != `{"limit":"5"}` {
		t.Fatalf("another tool was touched: %q", got)
	}
}

func TestReadUsageEventPerFormat(t *testing.T) {
	cases := []struct {
		format, ev string
		prompt     int64
	}{
		{FormatOpenAI, `{"usage":{"prompt_tokens":7,"completion_tokens":1}}`, 7},
		{FormatClaude, `{"type":"message_start","message":{"usage":{"input_tokens":5,"cache_read_input_tokens":2}}}`, 7},
		{FormatResponses, `{"type":"response.completed","response":{"usage":{"input_tokens":9,"output_tokens":1}}}`, 9},
		{FormatGemini, `{"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":2}}`, 11},
		{FormatOllama, `{"done":true,"prompt_eval_count":13,"eval_count":2}`, 13},
	}
	for _, c := range cases {
		u, ok := ReadUsageEvent(c.format, []byte(c.ev))
		if !ok || u.Prompt != c.prompt {
			t.Errorf("%s: %+v %v", c.format, u, ok)
		}
	}
	if _, ok := ReadUsageEvent(FormatClaude, []byte(`{"type":"ping"}`)); ok {
		t.Fatal("an event with no usage reported one")
	}
}

func FuzzStreamReaders(f *testing.F) {
	for _, s := range []string{
		`{"choices":[{"delta":{"content":"a","tool_calls":[{"index":0,"function":{"name":"f"}}]},"finish_reason":"stop"}]}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{"}}`,
		`{"type":"response.output_item.added","item":{"type":"function_call","id":"i","call_id":"c","name":"n"}}`,
		`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"n","args":{}}}]},"finishReason":"STOP"}]}`,
		`{"message":{"content":"a","tool_calls":[{"function":{"name":"n","arguments":{}}}]},"done":true}`,
		`[`, `null`, `{"error":1}`,
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		for _, from := range Formats() {
			fc, _ := Lookup(from)
			r := fc.NewStreamReader(NewCtx("", from))
			chunks := r.Read("", []byte(in))
			chunks = append(chunks, r.Flush()...)
			for _, to := range Formats() {
				tc, _ := Lookup(to)
				w := tc.NewStreamWriter(NewCtx(to, from))
				for _, ch := range chunks {
					_ = w.Write(ch)
				}
				_ = w.Close()
			}
		}
	})
}
