package translate

import (
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/text"
)

// ResponsesCodec joins the OpenAI Responses API to the hub.
type ResponsesCodec struct{}

func init() { Register(ResponsesCodec{}) }

// Name is the format name.
func (ResponsesCodec) Name() string { return FormatResponses }

// maxCallID is the longest call id the Responses API accepts.
const maxCallID = 64

var responsesKnown = set("model", "input", "instructions", "tools", "tool_choice", "stream", "max_output_tokens",
	"temperature", "top_p", "user", "reasoning", "parallel_tool_calls", "text", "store")

// ReadRequest reads a Responses body.
func (ResponsesCodec) ReadRequest(body []byte, c *Ctx) (*Request, error) {
	if !validObject(body) {
		return nil, ErrBadBody
	}
	g := gjson.ParseBytes(body)
	req := &Request{Model: g.Get("model").String(), Stream: g.Get("stream").Bool()}

	if ins := g.Get("instructions").String(); ins != "" {
		req.Messages = append(req.Messages, Message{Role: RoleSystem, Content: TextPart(ins)})
	}
	items, ok := responsesItems(g.Get("input"))
	if !ok {
		return nil, ErrBadBody
	}

	var pending *Message
	var reasoning, encrypted string
	flush := func() {
		if pending != nil {
			req.Messages = append(req.Messages, *pending)
			pending = nil
		}
	}
	attach := func(m *Message) {
		if reasoning != "" {
			m.Reasoning = reasoning
		}
		if encrypted != "" {
			m.ReasoningSignature = encrypted
		}
		reasoning, encrypted = "", ""
	}
	var extraTools []gjson.Result

	for _, it := range items {
		typ := it.Get("type").String()
		if typ == "" && it.Get("role").Exists() {
			typ = "message"
		}
		switch typ {
		case "message":
			flush()
			m := Message{Role: Role(it.Get("role").String()), Content: readResponsesContent(it.Get("content"))}
			if m.Role == "developer" {
				m.Role = RoleSystem
			}
			if m.Role == RoleAssistant {
				attach(&m)
			} else {
				reasoning, encrypted = "", ""
			}
			req.Messages = append(req.Messages, m)
		case "function_call", "custom_tool_call":
			name := strings.TrimSpace(it.Get("name").String())
			if pending == nil {
				pending = &Message{Role: RoleAssistant}
				attach(pending)
			}
			if name == "" {
				continue
			}
			custom := typ == "custom_tool_call"
			args := it.Get("arguments")
			var text string
			if custom {
				c.CustomTools[name] = true
				in := it.Get("input")
				if in.Type == gjson.String {
					text = mustJSON(map[string]string{"input": in.String()})
				} else {
					text = mustJSON(map[string]string{"input": in.Raw})
				}
			} else {
				text = argsText(args)
				if text == "" {
					text = "{}"
				}
			}
			pending.ToolCalls = append(pending.ToolCalls, ToolCall{ID: it.Get("call_id").String(), Name: name, Arguments: text, Custom: custom})
		case "function_call_output", "custom_tool_call_output":
			flush()
			out := it.Get("output")
			text := out.String()
			if out.Type != gjson.String {
				text = out.Raw
			}
			req.Messages = append(req.Messages, Message{Role: RoleTool, ToolCallID: it.Get("call_id").String(), Content: TextPart(text)})
		case "additional_tools":
			extraTools = append(extraTools, it.Get("tools").Array()...)
		case "reasoning":
			if txt := responsesReasoningText(it); txt != "" {
				if reasoning != "" {
					reasoning += "\n"
				}
				reasoning += txt
			}
			if e := it.Get("encrypted_content").String(); e != "" {
				encrypted = e
			}
		}
	}
	flush()

	for _, t := range append(g.Get("tools").Array(), extraTools...) {
		if tool, ok := readResponsesTool(t, c); ok {
			req.Tools = append(req.Tools, tool)
		}
	}
	req.ToolChoice = readOpenAIToolChoice(g.Get("tool_choice"))
	if tc := g.Get("tool_choice"); tc.IsObject() && tc.Get("name").Exists() {
		req.ToolChoice = &ToolChoice{Mode: "tool", Name: tc.Get("name").String()}
	}
	req.Params.MaxTokens = intPtr(g.Get("max_output_tokens"))
	req.Params.Temperature = floatPtr(g.Get("temperature"))
	req.Params.TopP = floatPtr(g.Get("top_p"))
	req.Params.User = g.Get("user").String()
	if e := g.Get("reasoning.effort").String(); e != "" {
		req.Thinking = effortToThinking(e)
	}
	req.Extra = extraOf(body, responsesKnown)
	return req, nil
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// responsesItems normalizes `input`: a string is one user message and an array
// is its items. An empty input becomes a placeholder user turn, because an
// upstream refuses a request with no messages.
func responsesItems(in gjson.Result) ([]gjson.Result, bool) {
	switch {
	case !in.Exists() || in.Type == gjson.Null:
		return []gjson.Result{gjson.Parse(`{"type":"message","role":"user","content":"..."}`)}, true
	case in.Type == gjson.String:
		text := in.String()
		if strings.TrimSpace(text) == "" {
			text = "..."
		}
		return []gjson.Result{gjson.Parse(mustJSON(map[string]string{"type": "message", "role": "user", "content": text}))}, true
	case in.IsArray():
		items := in.Array()
		if len(items) == 0 {
			return []gjson.Result{gjson.Parse(`{"type":"message","role":"user","content":"..."}`)}, true
		}
		return items, true
	}
	return nil, false
}

func readResponsesContent(c gjson.Result) []Part {
	if c.Type == gjson.String {
		if c.String() == "" {
			return nil
		}
		return TextPart(c.String())
	}
	var parts []Part
	for _, p := range c.Array() {
		switch p.Get("type").String() {
		case "input_text", "output_text", "text":
			parts = append(parts, Part{Type: PartText, Text: p.Get("text").String()})
		case "input_image":
			u := p.Get("image_url").String()
			if u == "" {
				u = p.Get("file_id").String()
			}
			detail := p.Get("detail").String()
			if detail == "" {
				detail = "auto"
			}
			parts = append(parts, Part{Type: PartImage, URL: u, Detail: detail})
		case "input_file":
			parts = append(parts, Part{Type: PartFile, Data: p.Get("file_data").String(), Name: p.Get("filename").String(), URL: p.Get("file_id").String()})
		}
	}
	return parts
}

func responsesReasoningText(it gjson.Result) string {
	for _, field := range []string{"summary", "content"} {
		var texts []string
		for _, s := range it.Get(field).Array() {
			if t := s.Get("text").String(); t != "" {
				texts = append(texts, t)
			}
		}
		if len(texts) > 0 {
			return strings.Join(texts, "\n")
		}
	}
	return ""
}

// readResponsesTool reads a tool declaration. A hosted tool with no name cannot
// be a function on any other upstream and is dropped.
func readResponsesTool(t gjson.Result, c *Ctx) (Tool, bool) {
	if t.Get("function").Exists() {
		return readOpenAITool(t), true
	}
	name := strings.TrimSpace(t.Get("name").String())
	if name == "" {
		return Tool{}, false
	}
	if t.Get("type").String() == "custom" {
		c.CustomTools[name] = true
		hint := strings.TrimSpace(t.Get("format.syntax").String() + "\n" + t.Get("format.definition").String())
		desc := t.Get("description").String()
		if hint != "" {
			desc = strings.TrimSpace(desc + "\n" + hint)
		}
		return Tool{Name: name, Description: desc, Custom: true,
			Parameters: json.RawMessage(`{"type":"object","properties":{"input":{"type":"string"}},"required":["input"]}`)}, true
	}
	return Tool{Name: name, Description: t.Get("description").String(), Parameters: json.RawMessage(t.Get("parameters").Raw)}, true
}

// WriteRequest writes the hub as a Responses body. Chat messages become input
// items; the system turn becomes instructions; reasoning is carried back as a
// reasoning item so a store:false conversation keeps its continuity.
func (ResponsesCodec) WriteRequest(r *Request, c *Ctx) ([]byte, error) {
	o := obj{"model": r.Model, "store": false}
	var system []string
	var items []any
	for _, m := range r.Messages {
		switch {
		case m.Role == RoleSystem:
			if t := m.Text(); strings.TrimSpace(t) != "" {
				system = append(system, t)
			}
		case m.Role == RoleTool:
			items = append(items, obj{"type": "function_call_output", "call_id": clampID(m.ToolCallID), "output": m.Text()})
		case m.Role == RoleAssistant:
			if m.Reasoning != "" || m.ReasoningSignature != "" {
				items = append(items, reasoningItem(m))
			}
			if t := m.Text(); t != "" {
				items = append(items, obj{"type": "message", "role": "assistant", "content": []any{obj{"type": "output_text", "text": t}}})
			}
			for _, tc := range m.ToolCalls {
				if tc.Custom || c.CustomTools[tc.Name] {
					items = append(items, obj{"type": "custom_tool_call", "call_id": clampID(tc.ID), "name": tc.Name, "input": customInput(tc.Arguments)})
				} else {
					items = append(items, obj{"type": "function_call", "call_id": clampID(tc.ID), "name": tc.Name, "arguments": tc.Arguments})
				}
			}
		default:
			if content := responsesContent(m.Content); len(content) > 0 {
				items = append(items, obj{"type": "message", "role": string(m.Role), "content": content})
			}
		}
	}
	if len(system) > 0 {
		o["instructions"] = strings.Join(system, "\n")
	}
	if len(items) == 0 {
		items = []any{obj{"type": "message", "role": "user", "content": []any{obj{"type": "input_text", "text": "..."}}}}
	}
	o["input"] = items
	if len(r.Tools) > 0 {
		o["tools"] = responsesTools(r.Tools, c)
	}
	if tc := r.ToolChoice; tc != nil {
		if tc.Mode == "tool" {
			o["tool_choice"] = obj{"type": "function", "name": tc.Name}
		} else {
			o["tool_choice"] = tc.Mode
		}
	}
	if r.Stream {
		o["stream"] = true
	}
	p := r.Params
	if p.MaxTokens != nil {
		o["max_output_tokens"] = *p.MaxTokens
	}
	if p.Temperature != nil {
		o["temperature"] = *p.Temperature
	}
	if p.TopP != nil {
		o["top_p"] = *p.TopP
	}
	if p.User != "" {
		o["user"] = p.User
	}
	if r.Thinking.Set() && r.Thinking.Mode != "budget" {
		effort := r.Thinking.Level
		if r.Thinking.Mode == "none" {
			effort = "none"
		}
		if effort != "" {
			o["reasoning"] = obj{"effort": effort, "summary": "auto"}
			o["include"] = []string{"reasoning.encrypted_content"}
		}
	}
	return json.Marshal(withExtra(o, r.Extra))
}

func clampID(id string) string {
	return text.CutBytes(id, maxCallID)
}

func reasoningItem(m Message) obj {
	it := obj{"type": "reasoning", "summary": []any{}}
	if m.Reasoning != "" {
		it["summary"] = []any{obj{"type": "summary_text", "text": m.Reasoning}}
	}
	if m.ReasoningSignature != "" {
		it["encrypted_content"] = m.ReasoningSignature
	}
	return it
}

// customInput reads the single `input` string a custom tool call carries.
func customInput(args string) string {
	if v := gjson.Get(args, "input"); v.Exists() {
		if v.Type == gjson.String {
			return v.String()
		}
		return v.Raw
	}
	return args
}

func responsesContent(parts []Part) []any {
	var out []any
	for _, p := range parts {
		switch p.Type {
		case PartText:
			if strings.TrimSpace(p.Text) != "" {
				out = append(out, obj{"type": "input_text", "text": p.Text})
			}
		case PartImage:
			u := p.URL
			if u == "" && p.Data != "" {
				u = "data:" + p.MediaType + ";base64," + p.Data
			}
			img := obj{"type": "input_image", "image_url": u}
			if p.Detail != "" {
				img["detail"] = p.Detail
			}
			out = append(out, img)
		case PartFile:
			f := obj{"type": "input_file"}
			if p.Data != "" {
				f["file_data"] = p.Data
			}
			if p.Name != "" {
				f["filename"] = p.Name
			}
			if p.URL != "" {
				f["file_id"] = p.URL
			}
			out = append(out, f)
		}
	}
	return out
}

func responsesTools(tools []Tool, c *Ctx) []any {
	out := make([]any, 0, len(tools))
	for _, t := range tools {
		if len(t.Raw) > 0 {
			out = append(out, t.Raw)
			continue
		}
		params := t.Parameters
		if len(params) == 0 {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		out = append(out, obj{"type": "function", "name": t.Name, "description": t.Description, "parameters": params})
	}
	return out
}

// ReadResponse reads a Responses answer.
func (ResponsesCodec) ReadResponse(body []byte, c *Ctx) (*Response, error) {
	if !validObject(body) {
		return nil, ErrBadBody
	}
	g := gjson.ParseBytes(body)
	resp := &Response{ID: g.Get("id").String(), Model: g.Get("model").String(), Created: g.Get("created_at").Int()}
	msg := Message{Role: RoleAssistant}
	for _, it := range g.Get("output").Array() {
		switch it.Get("type").String() {
		case "message":
			for _, p := range it.Get("content").Array() {
				if t := p.Get("type").String(); t == "output_text" || t == "text" {
					msg.Content = append(msg.Content, Part{Type: PartText, Text: p.Get("text").String()})
				}
			}
		case "reasoning":
			if txt := responsesReasoningText(it); txt != "" {
				if msg.Reasoning != "" {
					msg.Reasoning += "\n"
				}
				msg.Reasoning += txt
			}
			if e := it.Get("encrypted_content").String(); e != "" {
				msg.ReasoningSignature = e
			}
		case "function_call":
			msg.ToolCalls = append(msg.ToolCalls, ToolCall{ID: it.Get("call_id").String(), Name: it.Get("name").String(), Arguments: argsText(it.Get("arguments"))})
		case "custom_tool_call":
			msg.ToolCalls = append(msg.ToolCalls, ToolCall{ID: it.Get("call_id").String(), Name: it.Get("name").String(), Custom: true,
				Arguments: mustJSON(map[string]string{"input": it.Get("input").String()})})
		}
	}
	resp.Message = msg
	finish := FinishStop
	if g.Get("status").String() == "incomplete" {
		finish = FinishLength
		if g.Get("incomplete_details.reason").String() == "content_filter" {
			finish = FinishFilter
		}
	}
	resp.FinishReason = FinishWithCalls(finish, len(msg.ToolCalls))
	resp.Usage = readResponsesUsage(g.Get("usage"))
	resp.Extra = extraOf(body, set("id", "object", "created_at", "model", "output", "status", "usage", "incomplete_details"))
	return resp, nil
}

func readResponsesUsage(u gjson.Result) Usage {
	if !u.Exists() {
		return Usage{}
	}
	return NormalizeUsage(Usage{
		Prompt: u.Get("input_tokens").Int(), Completion: u.Get("output_tokens").Int(),
		Cached:    u.Get("input_tokens_details.cached_tokens").Int(),
		Reasoning: u.Get("output_tokens_details.reasoning_tokens").Int(),
	})
}

// WriteResponse writes the hub answer as a Responses body.
func (ResponsesCodec) WriteResponse(r *Response, c *Ctx) ([]byte, error) {
	var output []any
	if r.Message.Reasoning != "" || r.Message.ReasoningSignature != "" {
		output = append(output, reasoningItem(r.Message))
	}
	if t := r.Message.Text(); t != "" || len(r.Message.ToolCalls) == 0 {
		output = append(output, obj{"type": "message", "role": "assistant", "status": "completed",
			"content": []any{obj{"type": "output_text", "text": t, "annotations": []any{}}}})
	}
	for _, tc := range r.Message.ToolCalls {
		if tc.Custom || c.CustomTools[tc.Name] {
			output = append(output, obj{"type": "custom_tool_call", "call_id": tc.ID, "name": tc.Name, "input": customInput(tc.Arguments), "status": "completed"})
		} else {
			output = append(output, obj{"type": "function_call", "call_id": tc.ID, "name": tc.Name, "arguments": tc.Arguments, "status": "completed"})
		}
	}
	status := "completed"
	o := obj{"id": r.ID, "object": "response", "created_at": r.Created, "model": r.Model, "status": status, "output": output}
	switch r.FinishReason {
	case FinishLength:
		o["status"] = "incomplete"
		o["incomplete_details"] = obj{"reason": "max_output_tokens"}
	case FinishFilter:
		o["status"] = "incomplete"
		o["incomplete_details"] = obj{"reason": "content_filter"}
	}
	u := obj{"input_tokens": r.Usage.Prompt, "output_tokens": r.Usage.Completion, "total_tokens": r.Usage.Prompt + r.Usage.Completion}
	if r.Usage.Cached > 0 {
		u["input_tokens_details"] = obj{"cached_tokens": r.Usage.Cached}
	}
	if r.Usage.Reasoning > 0 {
		u["output_tokens_details"] = obj{"reasoning_tokens": r.Usage.Reasoning}
	}
	o["usage"] = u
	return json.Marshal(withExtra(o, r.Extra))
}
