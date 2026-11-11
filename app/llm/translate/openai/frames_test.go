package openai

import (
	"strings"
	"testing"

	"rllmx/app/llm/translate"
)

func TestTheTextFrameIsTheBytesTheMapFrameWrote(t *testing.T) {
	cases := []struct {
		name            string
		role            bool
		text, reasoning string
	}{
		{"text", false, "hello", ""},
		{"first with role", true, "hi", ""},
		{"reasoning", false, "", "hmm"},
		{"both", true, "a", "b"},
		{"escapes", false, "line\n\"q\" <b> \u2028 \xff", "r\t&"},
	}
	for _, c := range cases {
		w := &openAIWriter{id: "chatcmpl-abc12345", model: "m\"x", created: 1700000000}
		delta := translate.Obj{}
		if c.role {
			delta["role"] = "assistant"
		}
		if c.text != "" {
			delta["content"] = c.text
		}
		if c.reasoning != "" {
			delta["reasoning_content"] = c.reasoning
		}
		want := w.frame(delta, nil, nil)
		got := w.textFrame(c.role, c.text, c.reasoning)
		if string(got.Data) != string(want.Data) || got.Event != want.Event || got.Done != want.Done {
			t.Errorf("%s:\n got  %s\n want %s", c.name, got.Data, want.Data)
		}
	}
}

func TestOnlyTheFirstChunkCarriesTheRole(t *testing.T) {
	w := OpenAICodec{}.NewStreamWriter(translate.NewCtx(translate.FormatOpenAI, translate.FormatOpenAI))
	var frames []translate.Frame
	for _, c := range []translate.Chunk{{ID: "chatcmpl-abc12345", Model: "m", Text: "a"}, {Text: "b"}, {Reasoning: "r"}} {
		out := w.Write(c)
		frames = append(frames, out...)
	}
	if len(frames) != 3 {
		t.Fatalf("%d frames", len(frames))
	}
	for i, wantRole := range []bool{true, false, false} {
		if has := strings.Contains(string(frames[i].Data), `"role"`); has != wantRole {
			t.Errorf("frame %d role=%v: %s", i, has, frames[i].Data)
		}
	}
	if !strings.Contains(string(frames[1].Data), `"content":"b"`) || !strings.Contains(string(frames[2].Data), `"reasoning_content":"r"`) {
		t.Errorf("frames: %s | %s", frames[1].Data, frames[2].Data)
	}
}

// A repeated tool-call id opens one block: the reader keeps the id and a second
// fragment of the same call carries none, so a writer must not open a new call.
func TestARepeatedToolCallIDOpensOneBlock(t *testing.T) {
	c, _ := translate.Lookup(translate.FormatOpenAI)
	r := c.NewStreamReader(translate.NewCtx("", translate.FormatOpenAI))
	first := r.Read("", []byte(`{"id":"x","choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"f","arguments":"{"}}]}}]}`))
	again := r.Read("", []byte(`{"id":"x","choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"arguments":"}"}}]}}]}`))
	if first[0].ToolCalls[0].ID != "c1" || again[0].ToolCalls[0].ID != "" {
		t.Fatalf("%+v %+v", first, again)
	}
}
