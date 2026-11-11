package cursor

import (
	"encoding/json"
	"errors"
	"testing"

	"rllmx/app/llm/translate"
)

// cursorCtx is the hub context a writer sees for this provider.
func cursorCtx() *translate.Ctx {
	return translate.NewCtx(translate.FormatOpenAI, translate.FormatCursor)
}

func TestWriteRequestFlattensTheConversation(t *testing.T) {
	temp := 0.2
	max := 100
	req := &translate.Request{
		Model: "claude-4.5-sonnet",
		Messages: []translate.Message{
			{Role: translate.RoleSystem, Content: translate.TextPart("be brief")},
			{Role: translate.RoleUser, Content: translate.TextPart("hi")},
			{Role: translate.RoleAssistant, Content: translate.TextPart("one"),
				ToolCalls: []translate.ToolCall{{ID: "c1", Name: "read", Arguments: `{"p":"a"}`}}},
			{Role: translate.RoleTool, ToolCallID: "c1", Name: "read", Content: translate.TextPart("contents")},
			{Role: translate.RoleUser, Content: translate.TextPart("again")},
		},
		Tools:  []translate.Tool{{Name: "read", Description: "read", Parameters: json.RawMessage(`{"type":"object"}`)}},
		Params: translate.Params{Temperature: &temp, MaxTokens: &max},
	}
	body, err := Codec{}.WriteRequest(req, cursorCtx())
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	var out struct {
		Model     string `json:"model"`
		MaxTokens int    `json:"max_tokens"`
		Messages  []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("body is not JSON: %v (%s)", err, body)
	}
	if out.Model != "claude-4.5-sonnet" || out.MaxTokens != 32000 {
		t.Fatalf("model %q, max tokens %d", out.Model, out.MaxTokens)
	}
	want := []struct{ role, content string }{
		{"user", "[System Instructions]\nbe brief"},
		{"user", "hi"},
		{"assistant", "one"},
		{"user", "<tool_result>\n<tool_name>read</tool_name>\n<tool_call_id>c1</tool_call_id>\n<result>contents</result>\n</tool_result>"},
		{"user", "again"},
	}
	if len(out.Messages) != len(want) {
		t.Fatalf("messages = %+v", out.Messages)
	}
	for i, w := range want {
		if out.Messages[i].Role != w.role || out.Messages[i].Content != w.content {
			t.Errorf("message %d = %+v, want %+v", i, out.Messages[i], w)
		}
	}
}

func TestWriteRequestRefusesAnImage(t *testing.T) {
	req := &translate.Request{
		Model: "m",
		Messages: []translate.Message{{Role: translate.RoleUser, Content: []translate.Part{
			{Type: translate.PartText, Text: "look"},
			{Type: translate.PartImage, URL: "https://x.example/a.png"},
		}}},
	}
	if _, err := (Codec{}).WriteRequest(req, cursorCtx()); !errors.Is(err, translate.ErrImages) {
		t.Fatalf("err = %v, want ErrImages", err)
	}
}

func TestWriteRequestStripsControlCharactersFromAToolResult(t *testing.T) {
	req := &translate.Request{Model: "m", Messages: []translate.Message{
		{Role: translate.RoleTool, ToolCallID: "c1", Name: "t", Content: translate.TextPart("a\x00b\x07c")},
	}}
	body, err := Codec{}.WriteRequest(req, cursorCtx())
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	var out struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(body, &out)
	if len(out.Messages) != 1 || out.Messages[0].Content != "<tool_result>\n<tool_name>t</tool_name>\n<tool_call_id>c1</tool_call_id>\n<result>abc</result>\n</tool_result>" {
		t.Fatalf("messages = %+v", out.Messages)
	}
}

func TestTheCursorFormatRefusesAClientBody(t *testing.T) {
	if _, err := (Codec{}).ReadRequest([]byte(`{}`), cursorCtx()); err != translate.ErrBadBody {
		t.Fatalf("ReadRequest = %v", err)
	}
	if _, err := (Codec{}).ReadResponse([]byte(`{}`), cursorCtx()); err != translate.ErrBadBody {
		t.Fatalf("ReadResponse = %v", err)
	}
	if !(Codec{}).ReadOnly() || (Codec{}).Name() != translate.FormatCursor {
		t.Fatal("the format must be upstream-only and named cursor")
	}
}

func FuzzCursorWriteRequest(f *testing.F) {
	f.Add([]byte(`{"messages":[{"role":"user","content":"hi"}]}`))
	f.Add([]byte(`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"x"}}]}]}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		codec, ok := translate.Lookup(translate.FormatOpenAI)
		if !ok {
			t.Fatal("no OpenAI codec")
		}
		req, err := codec.ReadRequest(raw, translate.NewCtx(translate.FormatOpenAI, translate.FormatOpenAI))
		if err != nil {
			return
		}
		_, _ = (Codec{}).WriteRequest(req, cursorCtx())
	})
}
