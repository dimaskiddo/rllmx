package translate

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestAppendJSONStringIsWhatEncodingJSONWrites(t *testing.T) {
	for _, s := range []string{
		"", "plain", `quote " and \ slash`, "a\nb\r\tc\b\f", "\x00\x01\x1f\x7f", "<tag>&amp;", "caf\u00e9 \u4e16\u754c \U0001F600",
		"line\u2028sep\u2029para", "bad \xff\xfe bytes", "cut \xe2\x82", "\xed\xa0\x80 surrogate", "mix \"\u2028\xff<\n",
	} {
		want, _ := json.Marshal(s)
		if got := appendJSONString(nil, s); string(got) != string(want) {
			t.Errorf("%q:\n got  %s\n want %s", s, got, want)
		}
	}
}

func FuzzAppendJSONString(f *testing.F) {
	for _, s := range []string{"", "a", "\u2028", "\xff", "<>&\"\\\n"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		want, _ := json.Marshal(s)
		prefix := []byte("keep:")
		got := appendJSONString(prefix, s)
		if string(got) != "keep:"+string(want) {
			t.Fatalf("%q:\n got  %s\n want %s", s, got, want)
		}
	})
}

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
		delta := obj{}
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
	w := OpenAICodec{}.NewStreamWriter(NewCtx(FormatOpenAI, FormatOpenAI))
	var frames []Frame
	for _, c := range []Chunk{{ID: "chatcmpl-abc12345", Model: "m", Text: "a"}, {Text: "b"}, {Reasoning: "r"}} {
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

func TestTheClaudeDeltaFrameIsTheBytesTheMapFrameWrote(t *testing.T) {
	for _, c := range []struct{ kind, field, text string }{
		{"text_delta", "text", "hello"},
		{"text_delta", "text", "q\"<>&\n\u2028 \xff"},
		{"thinking_delta", "thinking", "hmm"},
	} {
		want := claudeFrame("content_block_delta", obj{"index": 3, "delta": obj{"type": c.kind, c.field: c.text}})
		got := claudeDeltaFrame(3, c.kind, c.field, c.text)
		if string(got.Data) != string(want.Data) || got.Event != want.Event {
			t.Errorf("%s:\n got  %s\n want %s", c.kind, got.Data, want.Data)
		}
	}
}

func TestTheResponsesDeltaEventIsTheBytesTheMapEventWrote(t *testing.T) {
	for _, c := range []struct{ name, key, text string }{
		{"response.output_text.delta", "content_index", "hello \"x\" <b>\n"},
		{"response.reasoning_summary_text.delta", "summary_index", "r\u2028\xff"},
	} {
		a, b := &responsesWriter{seq: 6}, &responsesWriter{seq: 6}
		want := a.ev(c.name, obj{"item_id": "msg_x_1", "output_index": 2, c.key: 0, "delta": c.text})
		got := b.textDeltaEvent(c.name, c.key, "msg_x_1", 2, c.text)
		if string(got.Data) != string(want.Data) || got.Event != want.Event || a.seq != b.seq {
			t.Errorf("%s:\n got  %s\n want %s\n seq %d vs %d", c.name, got.Data, want.Data, b.seq, a.seq)
		}
	}
}

func TestACallIDCutNeverSplitsACharacter(t *testing.T) {
	id := strings.Repeat("c", maxCallID-1) + "é" + "tail"
	got := clampID(id)
	if len(got) > maxCallID || !utf8.ValidString(got) {
		t.Fatalf("%d bytes, valid=%v", len(got), utf8.ValidString(got))
	}
	if clampID("call_1") != "call_1" {
		t.Fatal("a short id changed")
	}
}

func TestAStrictClientGetsAnIDItAccepts(t *testing.T) {
	for _, weak := range []string{"", "chat", "completion", "short", "1234567"} {
		if !WeakStreamID(weak) {
			t.Errorf("%q should be weak", weak)
		}
		got := StrictStreamID(weak)
		if !strings.HasPrefix(got, "chatcmpl-") || WeakStreamID(got) {
			t.Errorf("%q became %q", weak, got)
		}
	}
	for _, ok := range []string{"chatcmpl-abc12345", "resp_0123456789", "12345678"} {
		if WeakStreamID(ok) || StrictStreamID(ok) != ok {
			t.Errorf("%q was replaced", ok)
		}
	}
	first, second := StrictStreamID(""), StrictStreamID("")
	if first == second {
		t.Error("two generated ids are equal")
	}
}
