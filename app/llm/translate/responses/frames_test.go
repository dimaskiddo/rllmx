package responses

import (
	"strings"
	"testing"
	"unicode/utf8"

	"rllmx/app/llm/translate"
)

func TestTheResponsesDeltaEventIsTheBytesTheMapEventWrote(t *testing.T) {
	for _, c := range []struct{ name, key, text string }{
		{"response.output_text.delta", "content_index", "hello \"x\" <b>\n"},
		{"response.reasoning_summary_text.delta", "summary_index", "r\u2028\xff"},
	} {
		a, b := &responsesWriter{seq: 6}, &responsesWriter{seq: 6}
		want := a.ev(c.name, translate.Obj{"item_id": "msg_x_1", "output_index": 2, c.key: 0, "delta": c.text})
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
