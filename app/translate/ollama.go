package translate

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
)

// OllamaCodec joins Ollama's /api/chat to the hub.
type OllamaCodec struct{}

func init() { Register(OllamaCodec{}) }

// Name is the format name.
func (OllamaCodec) Name() string { return FormatOllama }

var ollamaKnown = set("model", "messages", "tools", "stream", "options", "think", "format", "keep_alive")

// ReadRequest reads an /api/chat body.
func (OllamaCodec) ReadRequest(body []byte, c *Ctx) (*Request, error) {
	if !validObject(body) {
		return nil, ErrBadBody
	}
	g := gjson.ParseBytes(body)
	// Ollama streams unless the body says otherwise (divergence 191).
	stream := true
	if s := g.Get("stream"); s.Exists() {
		stream = s.Bool()
	}
	req := &Request{Model: g.Get("model").String(), Stream: stream}
	msgs := g.Get("messages")
	if msgs.Exists() && !msgs.IsArray() {
		return nil, ErrBadBody
	}
	for _, m := range msgs.Array() {
		if !m.IsObject() {
			return nil, ErrBadBody
		}
		msg := Message{Role: Role(m.Get("role").String()), Reasoning: m.Get("thinking").String()}
		if msg.Role == "developer" {
			msg.Role = RoleSystem
		}
		if t := m.Get("content").String(); t != "" {
			msg.Content = TextPart(t)
		}
		for _, img := range m.Get("images").Array() {
			msg.Content = append(msg.Content, Part{Type: PartImage, MediaType: "image/png", Data: img.String()})
		}
		if msg.Role == RoleTool {
			msg.Name = m.Get("tool_name").String()
		}
		for i, tc := range m.Get("tool_calls").Array() {
			args := tc.Get("function.arguments").Raw
			if args == "" {
				args = "{}"
			}
			msg.ToolCalls = append(msg.ToolCalls, ToolCall{ID: fmt.Sprintf("call_%d", i), Name: tc.Get("function.name").String(), Arguments: args})
		}
		req.Messages = append(req.Messages, msg)
	}
	// A tool result names its tool, not a call id: pair it with the oldest
	// unanswered call of that name.
	pairOllamaResults(req)

	for _, t := range g.Get("tools").Array() {
		req.Tools = append(req.Tools, readOpenAITool(t))
	}
	o := g.Get("options")
	p := &req.Params
	p.Temperature = floatPtr(o.Get("temperature"))
	p.TopP = floatPtr(o.Get("top_p"))
	p.TopK = intPtr(o.Get("top_k"))
	p.MaxTokens = intPtr(o.Get("num_predict"))
	p.Seed = int64Ptr(o.Get("seed"))
	p.FrequencyPenalty = floatPtr(o.Get("frequency_penalty"))
	p.PresencePenalty = floatPtr(o.Get("presence_penalty"))
	p.Stop = strList(o.Get("stop"))
	switch th := g.Get("think"); {
	case th.Type == gjson.False:
		req.Thinking = Thinking{Mode: "none"}
	case th.Type == gjson.True:
		req.Thinking = Thinking{Mode: "auto"}
	case th.Type == gjson.String:
		req.Thinking = effortToThinking(th.String())
	}
	if f := g.Get("format"); f.Exists() {
		if f.Type == gjson.String && f.String() == "json" {
			p.ResponseFormat = json.RawMessage(`{"type":"json_object"}`)
		} else if f.IsObject() {
			p.ResponseFormat = json.RawMessage(mustJSON(obj{"type": "json_schema", "json_schema": obj{"name": "response", "schema": json.RawMessage(f.Raw)}}))
		}
	}
	req.Extra = extraOf(body, ollamaKnown)
	return req, nil
}

func pairOllamaResults(r *Request) {
	type open struct{ id, name string }
	var pending []open
	for i := range r.Messages {
		m := &r.Messages[i]
		for j := range m.ToolCalls {
			tc := &m.ToolCalls[j]
			tc.ID = fmt.Sprintf("call_%d_%d", i, j)
			pending = append(pending, open{id: tc.ID, name: tc.Name})
		}
		if m.Role == RoleTool {
			for k, o := range pending {
				if o.name == m.Name || m.Name == "" {
					m.ToolCallID = o.id
					pending = append(pending[:k], pending[k+1:]...)
					break
				}
			}
			if m.ToolCallID == "" {
				m.ToolCallID = fmt.Sprintf("call_orphan_%d", i)
			}
		}
	}
}

// WriteRequest writes the hub as an /api/chat body. Content is a string, images
// are raw base64, a tool result names its tool, and the fields Ollama has no
// place for (top_k aside) are dropped.
func (OllamaCodec) WriteRequest(r *Request, c *Ctx) ([]byte, error) {
	names := map[string]string{}
	for _, m := range r.Messages {
		for _, tc := range m.ToolCalls {
			names[tc.ID] = tc.Name
		}
	}
	var msgs []any
	for _, m := range r.Messages {
		role := string(m.Role)
		switch m.Role {
		case RoleTool:
			text := m.Text()
			if strings.TrimSpace(text) == "" {
				continue
			}
			msgs = append(msgs, obj{"role": "tool", "tool_name": firstNonEmptyStr(names[m.ToolCallID], m.Name, "unknown_tool"), "content": text})
			continue
		case RoleAssistant:
			w := obj{"role": role, "content": m.Text()}
			if m.Reasoning != "" {
				w["thinking"] = m.Reasoning
			}
			if len(m.ToolCalls) > 0 {
				calls := make([]any, 0, len(m.ToolCalls))
				for i, tc := range m.ToolCalls {
					calls = append(calls, obj{"type": "function", "function": obj{"index": i, "name": tc.Name, "arguments": rawOrEmpty(tc.Arguments)}})
				}
				w["tool_calls"] = calls
			}
			msgs = append(msgs, w)
			continue
		}
		text := m.Text()
		images := ollamaImages(m.Content)
		if text == "" && len(images) == 0 {
			continue
		}
		w := obj{"role": role, "content": text}
		if len(images) > 0 {
			w["images"] = images
		}
		msgs = append(msgs, w)
	}
	o := obj{"model": r.Model, "messages": msgs, "stream": r.Stream}
	opts := obj{}
	p := r.Params
	if p.Temperature != nil {
		opts["temperature"] = *p.Temperature
	}
	if p.TopP != nil {
		opts["top_p"] = *p.TopP
	}
	if p.TopK != nil {
		opts["top_k"] = *p.TopK
	}
	if p.MaxTokens != nil {
		opts["num_predict"] = *p.MaxTokens
	}
	if p.Seed != nil {
		opts["seed"] = *p.Seed
	}
	if p.FrequencyPenalty != nil {
		opts["frequency_penalty"] = *p.FrequencyPenalty
	}
	if p.PresencePenalty != nil {
		opts["presence_penalty"] = *p.PresencePenalty
	}
	if len(p.Stop) > 0 {
		opts["stop"] = p.Stop
	}
	if len(opts) > 0 {
		o["options"] = opts
	}
	if len(r.Tools) > 0 {
		var tools []any
		for _, t := range r.Tools {
			if len(t.Raw) > 0 {
				continue
			}
			params := t.Parameters
			if len(params) == 0 {
				params = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			tools = append(tools, obj{"type": "function", "function": obj{"name": t.Name, "description": t.Description, "parameters": params}})
		}
		if len(tools) > 0 {
			o["tools"] = tools
		}
	}
	switch r.Thinking.Mode {
	case "none":
		o["think"] = false
	case "auto":
		o["think"] = true
	case "level":
		o["think"] = r.Thinking.Level
	case "budget":
		o["think"] = true
	}
	if len(p.ResponseFormat) > 0 {
		var rf struct {
			Type       string `json:"type"`
			JSONSchema struct {
				Schema json.RawMessage `json:"schema"`
			} `json:"json_schema"`
		}
		if json.Unmarshal(p.ResponseFormat, &rf) == nil {
			if rf.Type == "json_schema" && len(rf.JSONSchema.Schema) > 0 {
				o["format"] = rf.JSONSchema.Schema
			} else if rf.Type == "json_object" || rf.Type == "json_schema" {
				o["format"] = "json"
			}
		}
	}
	// Options with no hub equivalent (num_ctx, keep_alive, ...) and tool_choice
	// are not forwarded: the endpoint has no field for the second and the first
	// belong to the Ollama client's own server.
	return json.Marshal(withExtra(o, r.Extra))
}

// ollamaImages are the raw base64 strings of a message's images. Ollama takes
// no URL, so a remote image is dropped here; the pipeline inlines it first.
func ollamaImages(parts []Part) []string {
	var out []string
	for _, p := range parts {
		if p.Type != PartImage {
			continue
		}
		switch {
		case p.Data != "":
			out = append(out, p.Data)
		case strings.HasPrefix(p.URL, "data:"):
			if _, d, ok := strings.Cut(p.URL, ","); ok {
				out = append(out, d)
			}
		}
	}
	return out
}

// ReadResponse reads a non-stream /api/chat answer.
func (OllamaCodec) ReadResponse(body []byte, c *Ctx) (*Response, error) {
	if !validObject(body) {
		return nil, ErrBadBody
	}
	g := gjson.ParseBytes(body)
	resp := &Response{Model: g.Get("model").String()}
	msg := Message{Role: RoleAssistant, Reasoning: g.Get("message.thinking").String()}
	if t := g.Get("message.content").String(); t != "" {
		msg.Content = TextPart(t)
	}
	for i, tc := range g.Get("message.tool_calls").Array() {
		args := tc.Get("function.arguments").Raw
		if args == "" {
			args = "{}"
		}
		msg.ToolCalls = append(msg.ToolCalls, ToolCall{ID: fmt.Sprintf("call_%d", i), Name: tc.Get("function.name").String(), Arguments: args})
	}
	resp.Message = msg
	finish := FinishStop
	switch g.Get("done_reason").String() {
	case "length":
		finish = FinishLength
	}
	resp.FinishReason = FinishWithCalls(finish, len(msg.ToolCalls))
	resp.Usage = NormalizeUsage(Usage{Prompt: g.Get("prompt_eval_count").Int(), Completion: g.Get("eval_count").Int()})
	resp.Extra = extraOf(body, set("model", "created_at", "message", "done", "done_reason", "prompt_eval_count", "eval_count",
		"total_duration", "load_duration", "prompt_eval_duration", "eval_duration"))
	return resp, nil
}

// WriteResponse writes the hub answer as an Ollama body: one object with
// done:true, a done_reason, counts, and content that is "" when the reply is
// empty.
func (OllamaCodec) WriteResponse(r *Response, c *Ctx) ([]byte, error) {
	msg := obj{"role": "assistant", "content": r.Message.Text()}
	if r.Message.Reasoning != "" {
		msg["thinking"] = r.Message.Reasoning
	}
	if len(r.Message.ToolCalls) > 0 {
		calls := make([]any, 0, len(r.Message.ToolCalls))
		for i, tc := range r.Message.ToolCalls {
			calls = append(calls, obj{"function": obj{"index": i, "name": tc.Name, "arguments": rawOrEmpty(tc.Arguments)}})
		}
		msg["tool_calls"] = calls
	}
	reason := "stop"
	if r.FinishReason == FinishLength {
		reason = "length"
	}
	o := obj{
		"model": r.Model, "message": msg, "done": true, "done_reason": reason,
		"prompt_eval_count": r.Usage.Prompt, "eval_count": r.Usage.Completion,
	}
	return json.Marshal(withExtra(o, r.Extra))
}
