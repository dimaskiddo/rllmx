package ollama

import (
	"cmp"
	"encoding/json"
	"fmt"
	"rllmx/app/llm/translate"
	"strings"

	"github.com/tidwall/gjson"
)

// OllamaCodec joins Ollama's /api/chat to the hub.
type OllamaCodec struct{}

func init() { translate.Register(OllamaCodec{}) }

// Name is the format name.
func (OllamaCodec) Name() string { return translate.FormatOllama }

var ollamaKnown = translate.Set("model", "messages", "tools", "stream", "options", "think", "format", "keep_alive")

// ReadRequest reads an /api/chat body.
func (OllamaCodec) ReadRequest(body []byte, c *translate.Ctx) (*translate.Request, error) {
	if !translate.ValidObject(body) {
		return nil, translate.ErrBadBody
	}
	g := gjson.ParseBytes(body)
	// Ollama streams unless the body says otherwise (divergence 191).
	stream := true
	if s := g.Get("stream"); s.Exists() {
		stream = s.Bool()
	}
	req := &translate.Request{Model: g.Get("model").String(), Stream: stream}
	msgs := g.Get("messages")
	if msgs.Exists() && !msgs.IsArray() {
		return nil, translate.ErrBadBody
	}
	for _, m := range msgs.Array() {
		if !m.IsObject() {
			return nil, translate.ErrBadBody
		}
		msg := translate.Message{Role: translate.Role(m.Get("role").String()), Reasoning: m.Get("thinking").String()}
		if msg.Role == "developer" {
			msg.Role = translate.RoleSystem
		}
		if t := m.Get("content").String(); t != "" {
			msg.Content = translate.TextPart(t)
		}
		for _, img := range m.Get("images").Array() {
			msg.Content = append(msg.Content, translate.Part{Type: translate.PartImage, MediaType: "image/png", Data: img.String()})
		}
		if msg.Role == translate.RoleTool {
			msg.Name = m.Get("tool_name").String()
		}
		for i, tc := range m.Get("tool_calls").Array() {
			args := tc.Get("function.arguments").Raw
			if args == "" {
				args = "{}"
			}
			msg.ToolCalls = append(msg.ToolCalls, translate.ToolCall{ID: fmt.Sprintf("call_%d", i), Name: tc.Get("function.name").String(), Arguments: args})
		}
		req.Messages = append(req.Messages, msg)
	}
	// A tool result names its tool, not a call id: pair it with the oldest
	// unanswered call of that name.
	pairOllamaResults(req)

	for _, t := range g.Get("tools").Array() {
		req.Tools = append(req.Tools, translate.ReadOpenAITool(t))
	}
	o := g.Get("options")
	p := &req.Params
	p.Temperature = translate.FloatPtr(o.Get("temperature"))
	p.TopP = translate.FloatPtr(o.Get("top_p"))
	p.TopK = translate.IntPtr(o.Get("top_k"))
	p.MaxTokens = translate.IntPtr(o.Get("num_predict"))
	p.Seed = translate.Int64Ptr(o.Get("seed"))
	p.FrequencyPenalty = translate.FloatPtr(o.Get("frequency_penalty"))
	p.PresencePenalty = translate.FloatPtr(o.Get("presence_penalty"))
	p.Stop = translate.StrList(o.Get("stop"))
	switch th := g.Get("think"); {
	case th.Type == gjson.False:
		req.Thinking = translate.Thinking{Mode: "none"}
	case th.Type == gjson.True:
		req.Thinking = translate.Thinking{Mode: "auto"}
	case th.Type == gjson.String:
		req.Thinking = translate.EffortToThinking(th.String())
	}
	if f := g.Get("format"); f.Exists() {
		if f.Type == gjson.String && f.String() == "json" {
			p.ResponseFormat = json.RawMessage(`{"type":"json_object"}`)
		} else if f.IsObject() {
			p.ResponseFormat = json.RawMessage(translate.MustJSON(translate.Obj{"type": "json_schema", "json_schema": translate.Obj{"name": "response", "schema": json.RawMessage(f.Raw)}}))
		}
	}
	req.Extra = translate.ExtraOf(body, ollamaKnown)
	return req, nil
}

func pairOllamaResults(r *translate.Request) {
	type open struct{ id, name string }
	var pending []open
	for i := range r.Messages {
		m := &r.Messages[i]
		for j := range m.ToolCalls {
			tc := &m.ToolCalls[j]
			tc.ID = fmt.Sprintf("call_%d_%d", i, j)
			pending = append(pending, open{id: tc.ID, name: tc.Name})
		}
		if m.Role == translate.RoleTool {
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
func (OllamaCodec) WriteRequest(r *translate.Request, c *translate.Ctx) ([]byte, error) {
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
		case translate.RoleTool:
			text := m.Text()
			if strings.TrimSpace(text) == "" {
				continue
			}
			msgs = append(msgs, translate.Obj{"role": "tool", "tool_name": cmp.Or(names[m.ToolCallID], m.Name, "unknown_tool"), "content": text})
			continue
		case translate.RoleAssistant:
			w := translate.Obj{"role": role, "content": m.Text()}
			if m.Reasoning != "" {
				w["thinking"] = m.Reasoning
			}
			if len(m.ToolCalls) > 0 {
				calls := make([]any, 0, len(m.ToolCalls))
				for i, tc := range m.ToolCalls {
					calls = append(calls, translate.Obj{"type": "function", "function": translate.Obj{"index": i, "name": tc.Name, "arguments": translate.RawOrEmpty(tc.Arguments)}})
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
		w := translate.Obj{"role": role, "content": text}
		if len(images) > 0 {
			w["images"] = images
		}
		msgs = append(msgs, w)
	}
	o := translate.Obj{"model": r.Model, "messages": msgs, "stream": r.Stream}
	opts := translate.Obj{}
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
			tools = append(tools, translate.Obj{"type": "function", "function": translate.Obj{"name": t.Name, "description": t.Description, "parameters": params}})
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
	return json.Marshal(translate.WithExtra(o, r.Extra))
}

// ollamaImages are the raw base64 strings of a message's images. Ollama takes
// no URL, so a remote image is dropped here; the pipeline inlines it first.
func ollamaImages(parts []translate.Part) []string {
	var out []string
	for _, p := range parts {
		if p.Type != translate.PartImage {
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
func (OllamaCodec) ReadResponse(body []byte, c *translate.Ctx) (*translate.Response, error) {
	if !translate.ValidObject(body) {
		return nil, translate.ErrBadBody
	}
	g := gjson.ParseBytes(body)
	resp := &translate.Response{Model: g.Get("model").String()}
	msg := translate.Message{Role: translate.RoleAssistant, Reasoning: g.Get("message.thinking").String()}
	if t := g.Get("message.content").String(); t != "" {
		msg.Content = translate.TextPart(t)
	}
	for i, tc := range g.Get("message.tool_calls").Array() {
		args := tc.Get("function.arguments").Raw
		if args == "" {
			args = "{}"
		}
		msg.ToolCalls = append(msg.ToolCalls, translate.ToolCall{ID: fmt.Sprintf("call_%d", i), Name: tc.Get("function.name").String(), Arguments: args})
	}
	resp.Message = msg
	finish := translate.FinishStop
	switch g.Get("done_reason").String() {
	case "length":
		finish = translate.FinishLength
	}
	resp.FinishReason = translate.FinishWithCalls(finish, len(msg.ToolCalls))
	resp.Usage = translate.NormalizeUsage(translate.Usage{Prompt: g.Get("prompt_eval_count").Int(), Completion: g.Get("eval_count").Int()})
	resp.Extra = translate.ExtraOf(body, translate.Set("model", "created_at", "message", "done", "done_reason", "prompt_eval_count", "eval_count",
		"total_duration", "load_duration", "prompt_eval_duration", "eval_duration"))
	return resp, nil
}

// WriteResponse writes the hub answer as an Ollama body: one object with
// done:true, a done_reason, counts, and content that is "" when the reply is
// empty.
func (OllamaCodec) WriteResponse(r *translate.Response, c *translate.Ctx) ([]byte, error) {
	msg := translate.Obj{"role": "assistant", "content": r.Message.Text()}
	if r.Message.Reasoning != "" {
		msg["thinking"] = r.Message.Reasoning
	}
	if len(r.Message.ToolCalls) > 0 {
		calls := make([]any, 0, len(r.Message.ToolCalls))
		for i, tc := range r.Message.ToolCalls {
			calls = append(calls, translate.Obj{"function": translate.Obj{"index": i, "name": tc.Name, "arguments": translate.RawOrEmpty(tc.Arguments)}})
		}
		msg["tool_calls"] = calls
	}
	reason := "stop"
	if r.FinishReason == translate.FinishLength {
		reason = "length"
	}
	o := translate.Obj{
		"model": r.Model, "message": msg, "done": true, "done_reason": reason,
		"prompt_eval_count": r.Usage.Prompt, "eval_count": r.Usage.Completion,
	}
	return json.Marshal(translate.WithExtra(o, r.Extra))
}
