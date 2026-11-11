package cursor

import (
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"rllmx/app/llm/translate"
)

// Codec joins the Cursor agent service's body to the hub. The client's own
// conversation is written in the Chat Completions shape the service's
// translation reaches, then flattened: the service takes text turns and its own
// history lines, and loses tool results written any other way.
type Codec struct{}

func init() { translate.Register(Codec{}) }

// Name is the format name.
func (Codec) Name() string { return translate.FormatCursor }

// ReadOnly reports that no client speaks this format.
func (Codec) ReadOnly() bool { return true }

// ReadRequest refuses a conversation body: only an upstream speaks this format.
func (Codec) ReadRequest(body []byte, c *translate.Ctx) (*translate.Request, error) {
	return nil, translate.ErrBadBody
}

// WriteRequest writes the hub as the flattened body the agent service takes.
// An image is refused rather than dropped: the service's text path is the only
// one this router builds, and a missing picture is a wrong answer nobody sees.
func (Codec) WriteRequest(r *translate.Request, c *translate.Ctx) ([]byte, error) {
	if hubParts(r) {
		return nil, translate.ErrImages
	}
	raw, err := writeOpenAI(r, c)
	if err != nil {
		return nil, err
	}
	return cursorBody(raw, r.Model)
}

// hubParts reports the pictures a hub request carries, before any body is
// written, so the refusal costs no encoding.
func hubParts(r *translate.Request) bool {
	for _, m := range r.Messages {
		for _, p := range m.Content {
			if p.Type == translate.PartImage {
				return true
			}
		}
	}
	return false
}

// writeOpenAI is the Chat Completions body the hub writes, before flattening.
func writeOpenAI(r *translate.Request, c *translate.Ctx) ([]byte, error) {
	codec, ok := translate.Lookup(translate.FormatOpenAI)
	if !ok {
		return nil, translate.ErrBadBody
	}
	return codec.WriteRequest(r, c)
}

// cursorDrops are the fields the service never reads.
var cursorDrops = []string{"user", "metadata", "tool_choice", "stream_options", "system"}

// cursorMinTokens is what the service is always asked for; the reference sends
// the same floor whatever the client asked.
const cursorMinTokens = 32000

// cursorBody turns a Chat Completions body into the service's conversation:
// system text becomes a user turn, a tool result becomes a text block, and a
// Claude-shaped content array keeps its text and its calls.
func cursorBody(body []byte, model string) ([]byte, error) {
	if !gjson.ValidBytes(body) {
		return nil, translate.ErrBadBody
	}
	g := gjson.ParseBytes(body)
	if imageIn(g.Get("messages")) {
		return nil, translate.ErrImages
	}
	names := toolNames(g.Get("messages"))
	out, err := json.Marshal(translate.Obj{"model": model, "messages": cursorMessages(g.Get("messages"), names), "max_tokens": cursorMinTokens})
	if err != nil {
		return nil, translate.ErrBadBody
	}
	for _, path := range cursorDrops {
		out = remove(out, path)
	}
	return out, nil
}

// imageIn reports a picture anywhere in the conversation.
func imageIn(messages gjson.Result) bool {
	for _, m := range messages.Array() {
		for _, p := range m.Get("content").Array() {
			if t := p.Get("type").String(); t == "image_url" || t == "image" || t == "input_image" {
				return true
			}
			if p.Get("image_url").Exists() || p.Get("source").Exists() {
				return true
			}
		}
	}
	return false
}

// toolNames maps a call id to the tool it asked for, so a result carries a name
// the service can match.
func toolNames(messages gjson.Result) map[string]string {
	names := map[string]string{}
	for _, m := range messages.Array() {
		for _, tc := range m.Get("tool_calls").Array() {
			names[tc.Get("id").String()] = tc.Get("function.name").String()
		}
		for _, p := range m.Get("content").Array() {
			names[p.Get("id").String()] = p.Get("name").String()
		}
	}
	return names
}

// cursorMessages flattens the conversation to text turns.
func cursorMessages(messages gjson.Result, names map[string]string) []any {
	out := make([]any, 0, len(messages.Array())+1)
	for _, m := range messages.Array() {
		switch m.Get("role").String() {
		case "system":
			text := contentText(m.Get("content"))
			if text != "" {
				out = append(out, translate.Obj{"role": "user", "content": "[System Instructions]\n" + text})
			}
		case "tool":
			out = append(out, translate.Obj{"role": "user", "content": toolResult(
				m.Get("name").String(), names[m.Get("tool_call_id").String()], m.Get("tool_call_id").String(), contentText(m.Get("content")))})
		case "user", "assistant":
			out = append(out, cursorTurn(m, names)...)
		}
	}
	return out
}

// cursorTurn writes one user or assistant turn: its text, the results a Claude
// client put inside a user turn, and the calls an assistant made.
func cursorTurn(m gjson.Result, names map[string]string) []any {
	role := m.Get("role").String()
	var parts []string
	for _, p := range m.Get("content").Array() {
		switch p.Get("type").String() {
		case "text", "input_text":
			if t := p.Get("text").String(); t != "" {
				parts = append(parts, t)
			}
		case "tool_result":
			parts = append(parts, toolResult(p.Get("name").String(), names[p.Get("tool_use_id").String()], p.Get("tool_use_id").String(), contentText(p.Get("content"))))
		}
	}
	if text := contentText(m.Get("content")); text != "" {
		parts = []string{text}
	}
	text := strings.Join(parts, "\n")
	calls := cursorCalls(m)
	switch {
	case role == "assistant" && len(calls) > 0:
		return []any{translate.Obj{"role": role, "content": text, "tool_calls": calls}}
	case text != "":
		return []any{translate.Obj{"role": role, "content": text}}
	}
	return nil
}

// cursorCalls keeps the calls an assistant made, as the OpenAI shape the
// service reads, from a native field or from a Claude content block.
func cursorCalls(m gjson.Result) []any {
	var calls []any
	for _, tc := range m.Get("tool_calls").Array() {
		calls = append(calls, translate.Obj{"id": tc.Get("id").String(), "type": "function",
			"function": translate.Obj{"name": tc.Get("function.name").String(), "arguments": tc.Get("function.arguments").String()}})
	}
	for _, p := range m.Get("content").Array() {
		if p.Get("type").String() != "tool_use" {
			continue
		}
		args, _ := json.Marshal(p.Get("input").Value())
		calls = append(calls, translate.Obj{"id": p.Get("id").String(), "type": "function",
			"function": translate.Obj{"name": p.Get("name").String(), "arguments": string(args)}})
	}
	return calls
}

// contentText reads a message's text from a string or from its text parts.
func contentText(v gjson.Result) string {
	if v.Type == gjson.String {
		return v.String()
	}
	var parts []string
	for _, p := range v.Array() {
		switch p.Get("type").String() {
		case "text", "input_text":
			if t := p.Get("text").String(); t != "" {
				parts = append(parts, t)
			}
		}
	}
	return strings.Join(parts, "")
}

// toolResult is the text block the service reads a tool's answer from. The
// control characters a tool prints are stripped: the service answers a request
// that carries them with a parse error.
func toolResult(name, known, id, text string) string {
	if known == "" {
		known = name
	}
	if known == "" {
		known = "tool"
	}
	return "<tool_result>\n<tool_name>" + escapeXML(known) + "</tool_name>\n<tool_call_id>" +
		escapeXML(id) + "</tool_call_id>\n<result>" + escapeXML(cleanControl(text)) + "</result>\n</tool_result>"
}

// escapeXML keeps a tool's own text from closing the block early.
func escapeXML(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// cleanControl drops the control characters an XML block cannot carry.
func cleanControl(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r < 0x20 && r != '\n' && r != '\t' || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// remove deletes a path and keeps the body when the delete fails.
func remove(body []byte, path string) []byte {
	if next, err := sjson.DeleteBytes(body, path); err == nil {
		return next
	}
	return body
}
