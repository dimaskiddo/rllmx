package translate

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// conversation is one conversation in the hub, used to drive every pair.
func conversation() *Request {
	temp := 0.4
	max := 200
	return &Request{
		Model: "m",
		Messages: []Message{
			{Role: RoleSystem, Content: TextPart("You are helpful.")},
			{Role: RoleUser, Content: TextPart("What is the weather in Paris?")},
			{Role: RoleAssistant, Content: TextPart("Let me check."), ToolCalls: []ToolCall{{ID: "call_1", Name: "weather", Arguments: `{"city":"Paris"}`}}},
			{Role: RoleTool, ToolCallID: "call_1", Name: "weather", Content: TextPart("sunny, 22C")},
			{Role: RoleAssistant, Content: TextPart("It is sunny.")},
			{Role: RoleUser, Content: TextPart("Thanks!")},
		},
		Tools:  []Tool{{Name: "weather", Description: "get weather", Parameters: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`)}},
		Params: Params{Temperature: &temp, MaxTokens: &max},
	}
}

// fingerprint reduces a hub request to what every codec must preserve.
func fingerprint(r *Request) string {
	var b strings.Builder
	for _, m := range r.Messages {
		role := m.Role
		fmt.Fprintf(&b, "%s|%s|", role, strings.TrimSpace(m.Text()))
		for _, tc := range m.ToolCalls {
			fmt.Fprintf(&b, "call:%s:%s;", tc.Name, strings.ReplaceAll(tc.Arguments, " ", ""))
		}
		if m.Role == RoleTool {
			fmt.Fprintf(&b, "result;")
		}
		b.WriteString("\n")
	}
	for _, t := range r.Tools {
		fmt.Fprintf(&b, "tool:%s\n", t.Name)
	}
	return b.String()
}

func TestEveryPairPreservesTheConversation(t *testing.T) {
	formats := []string{FormatOpenAI, FormatClaude, FormatResponses, FormatGemini, FormatOllama}
	want := fingerprint(conversation())
	for _, from := range formats {
		src, _ := Lookup(from)
		body, err := src.WriteRequest(conversation(), NewCtx(FormatOpenAI, from))
		if err != nil {
			t.Fatalf("write %s: %v", from, err)
		}
		for _, to := range formats {
			c := NewCtx(from, to)
			out, req, err := Convert(from, to, body, c)
			if err != nil {
				t.Errorf("%s -> %s: %v", from, to, err)
				continue
			}
			if !json.Valid(out) {
				t.Errorf("%s -> %s: output is not JSON: %s", from, to, out)
			}
			// Roles can legitimately differ in what a system turn is called or
			// where merges happen, so compare the text and the calls in order.
			got := fingerprint(req)
			for _, must := range []string{"What is the weather in Paris?", "call:weather:" + `{"city":"Paris"}`, "tool:weather", "sunny, 22C", "Thanks!"} {
				if !strings.Contains(got, must) {
					t.Errorf("%s -> %s: lost %q\nwant like:\n%s\ngot:\n%s", from, to, must, want, got)
				}
			}
		}
	}
}

func TestEveryWriterProducesValidJSONForEveryReaderOutput(t *testing.T) {
	for _, name := range WriteFormats() {
		c, _ := Lookup(name)
		for _, req := range []*Request{conversation(), {}, {Model: "m"}, {Messages: []Message{user("x")}, Stream: true}} {
			out, err := c.WriteRequest(req, NewCtx(FormatOpenAI, name))
			if err != nil || !json.Valid(out) {
				t.Errorf("%s: %v %s", name, err, out)
			}
		}
		resp := &Response{ID: "r", Model: "m", Message: Message{Role: RoleAssistant, Content: TextPart("hi")}, Usage: Usage{Prompt: 5, Completion: 2}}
		out, err := c.WriteResponse(resp, NewCtx(FormatOpenAI, name))
		if err != nil || !json.Valid(out) {
			t.Errorf("%s response: %v %s", name, err, out)
		}
		// a response survives its own round trip
		back, err := c.ReadResponse(out, NewCtx(name, name))
		if err != nil {
			t.Errorf("%s: read back: %v", name, err)
			continue
		}
		if back.Message.Text() != "hi" {
			t.Errorf("%s: text %q", name, back.Message.Text())
		}
		if back.Usage.Prompt != 5 || back.Usage.Completion != 2 {
			t.Errorf("%s: usage %+v", name, back.Usage)
		}
	}
}

func TestEveryFormatIsRegistered(t *testing.T) {
	got := strings.Join(Formats(), ",")
	if got != "claude,commandcode,gemini,gemini-cli,kiro,ollama,openai,openai-responses,vertex" {
		t.Fatalf("formats %s", got)
	}
	if w := strings.Join(WriteFormats(), ","); w != "claude,gemini,gemini-cli,ollama,openai,openai-responses,vertex" {
		t.Fatalf("writable %s", w)
	}
}

func TestEveryResponseCarriesTheSameAnswerThroughAnyPair(t *testing.T) {
	resp := &Response{ID: "r", Model: "m", FinishReason: FinishToolCalls,
		Message: Message{Role: RoleAssistant, Content: TextPart("calling"), ToolCalls: []ToolCall{{ID: "c1", Name: "f", Arguments: `{"a":1}`}}},
		Usage:   Usage{Prompt: 30, Completion: 9, Cached: 10, Reasoning: 3}}
	for _, from := range WriteFormats() {
		src, _ := Lookup(from)
		body, err := src.WriteResponse(resp, NewCtx(FormatOpenAI, from))
		if err != nil {
			t.Fatal(err)
		}
		read, err := src.ReadResponse(body, NewCtx(from, from))
		if err != nil {
			t.Fatalf("%s: %v", from, err)
		}
		for _, to := range WriteFormats() {
			dst, _ := Lookup(to)
			out, err := dst.WriteResponse(read, NewCtx(from, to))
			if err != nil || !json.Valid(out) {
				t.Errorf("%s -> %s: %v %s", from, to, err, out)
				continue
			}
			back, err := dst.ReadResponse(out, NewCtx(to, to))
			if err != nil {
				t.Errorf("%s -> %s: %v", from, to, err)
				continue
			}
			if len(back.Message.ToolCalls) != 1 || back.Message.ToolCalls[0].Name != "f" {
				t.Errorf("%s -> %s: tool call lost: %+v", from, to, back.Message.ToolCalls)
			}
			if back.FinishReason != FinishToolCalls {
				t.Errorf("%s -> %s: finish %q", from, to, back.FinishReason)
			}
			if back.Usage.Prompt != 30 && to != FormatOllama {
				t.Errorf("%s -> %s: prompt %d", from, to, back.Usage.Prompt)
			}
		}
	}
}

func FuzzOpenAIReadRequest(f *testing.F)    { fuzzReader(f, OpenAICodec{}) }
func FuzzClaudeReadRequest(f *testing.F)    { fuzzReader(f, ClaudeCodec{}) }
func FuzzResponsesReadRequest(f *testing.F) { fuzzReader(f, ResponsesCodec{}) }
func FuzzGeminiReadRequest(f *testing.F)    { fuzzReader(f, GeminiCodec{}) }
func FuzzOllamaReadRequest(f *testing.F)    { fuzzReader(f, OllamaCodec{}) }

// fuzzReader feeds arbitrary bytes to a reader and, when one is accepted, to
// every writer: no body may panic a codec.
func fuzzReader(f *testing.F, c Codec) {
	seeds := []string{
		``, `{}`, `[]`, `null`, `{"messages":[]}`, `{"messages":[{"role":"user","content":"x"}]}`,
		`{"contents":[{"role":"user","parts":[{"text":"x"}]}]}`, `{"input":"x"}`, `{"input":[{"type":"function_call","name":"f"}]}`,
		`{"messages":[{"role":"assistant","tool_calls":[{"id":"1","function":{"name":"f","arguments":"{"}}]}]}`,
		`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:,"}}]}]}`,
		`{"model":"m","max_tokens":-1,"messages":[{"role":"tool","content":null}]}`,
		`{"contents":[{"parts":[{"functionResponse":{"name":"f"}}]}]}`, `{"tools":[{"input_schema":[]}]}`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		req, err := c.ReadRequest(body, NewCtx(c.Name(), FormatOpenAI))
		if err != nil || req == nil {
			return
		}
		for _, name := range WriteFormats() {
			w, _ := Lookup(name)
			ctx := NewCtx(c.Name(), name)
			Prepare(req, ctx)
			if out, err := w.WriteRequest(req, ctx); err == nil && !json.Valid(out) {
				t.Fatalf("%s wrote invalid JSON for an accepted body", name)
			}
		}
	})
}

func FuzzReadResponse(f *testing.F) {
	for _, s := range []string{``, `{}`, `[]`, `{"choices":[]}`, `{"candidates":[{}]}`, `{"output":[{}]}`, `{"content":[{}]}`, `{"message":{}}`,
		`{"usage":{"prompt_tokens":-5}}`, `{"choices":[{"message":{"tool_calls":[{}]}}]}`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		for _, name := range Formats() {
			c, _ := Lookup(name)
			resp, err := c.ReadResponse(body, NewCtx(name, name))
			if err != nil || resp == nil {
				continue
			}
			u := resp.Usage
			if u.Prompt < 0 || u.Completion < 0 || u.Cached+u.CacheWrite > u.Prompt || u.Reasoning > u.Completion {
				t.Fatalf("%s: usage invariants broken: %+v", name, u)
			}
			for _, to := range WriteFormats() {
				w, _ := Lookup(to)
				if out, err := w.WriteResponse(resp, NewCtx(name, to)); err == nil && !json.Valid(out) {
					t.Fatalf("%s -> %s wrote invalid JSON", name, to)
				}
			}
		}
	})
}
