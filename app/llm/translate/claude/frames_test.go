package claude

import (
	"testing"

	"rllmx/app/llm/translate"
)

// claudeCodec is the Claude codec through the registry: the format is this
// package, and a test asks for it the way production does.
func claudeCodec(t testing.TB) translate.Codec {
	t.Helper()
	c, ok := translate.Lookup(translate.FormatClaude)
	if !ok {
		t.Fatal("the claude codec is not registered")
	}
	return c
}

func TestTheClaudeDeltaFrameIsTheBytesTheMapFrameWrote(t *testing.T) {
	for _, c := range []struct{ kind, field, text string }{
		{"text_delta", "text", "hello"},
		{"text_delta", "text", "q\"<>&\n\u2028 \xff"},
		{"thinking_delta", "thinking", "hmm"},
	} {
		want := claudeFrame("content_block_delta", translate.Obj{"index": 3, "delta": translate.Obj{"type": c.kind, c.field: c.text}})
		got := claudeDeltaFrame(3, c.kind, c.field, c.text)
		if string(got.Data) != string(want.Data) || got.Event != want.Event {
			t.Errorf("%s:\n got  %s\n want %s", c.kind, got.Data, want.Data)
		}
	}
}
