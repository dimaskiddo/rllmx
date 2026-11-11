package claude

import (
	"testing"

	"rllmx/app/llm/translate"
)

func TestARepeatedToolCallIDOpensOneBlock(t *testing.T) {
	var out []translate.Frame
	c, ok := translate.Lookup(translate.FormatClaude)
	if !ok {
		t.Fatal("the claude codec is not registered")
	}
	w := c.NewStreamWriter(translate.NewCtx(translate.FormatClaude, translate.FormatClaude))
	for _, ch := range []translate.Chunk{
		{ID: "m", Model: "m", Role: translate.RoleAssistant, ToolCalls: []translate.ToolCallDelta{{Index: 0, ID: "c1", Name: "f", Arguments: "{"}}},
		{ToolCalls: []translate.ToolCallDelta{{Index: 0, ID: "c1", Arguments: "}"}}},
		{FinishReason: translate.FinishToolCalls, Usage: &translate.Usage{Prompt: 1, Completion: 1}},
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
