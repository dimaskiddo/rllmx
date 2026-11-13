package openai

import (
	"encoding/json"
	"rllmx/app/llm/translate"
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/upstream"
)

// OpenAICodec joins OpenAI Chat Completions to the hub.
type OpenAICodec struct{}

func init() { translate.Register(OpenAICodec{}) }

// Name is the format name.
func (OpenAICodec) Name() string { return translate.FormatOpenAI }

var openAIKnown = translate.Set("model", "messages", "tools", "tool_choice", "stream", "max_tokens", "max_completion_tokens",
	"temperature", "top_p", "stop", "seed", "frequency_penalty", "presence_penalty", "user", "response_format",
	"reasoning_effort", "stream_options")

// ReadRequest reads a Chat Completions body.
func (OpenAICodec) ReadRequest(body []byte, c *translate.Ctx) (*translate.Request, error) {
	if !translate.ValidObject(body) {
		return nil, translate.ErrBadBody
	}
	g := gjson.ParseBytes(body)
	req := &translate.Request{Model: g.Get("model").String(), Stream: g.Get("stream").Bool()}
	if msgs := g.Get("messages"); msgs.Exists() {
		if !msgs.IsArray() {
			return nil, translate.ErrBadBody
		}
		for _, m := range msgs.Array() {
			if !m.IsObject() {
				return nil, translate.ErrBadBody
			}
			req.Messages = append(req.Messages, ReadOpenAIMessage(m))
		}
	}
	for _, t := range g.Get("tools").Array() {
		req.Tools = append(req.Tools, translate.ReadOpenAITool(t))
	}
	req.ToolChoice = readOpenAIToolChoice(g.Get("tool_choice"))
	p := &req.Params
	if v := g.Get("max_completion_tokens"); v.Exists() {
		p.MaxTokens = translate.IntPtr(v)
	} else {
		p.MaxTokens = translate.IntPtr(g.Get("max_tokens"))
	}
	p.Temperature = translate.FloatPtr(g.Get("temperature"))
	p.TopP = translate.FloatPtr(g.Get("top_p"))
	p.Seed = translate.Int64Ptr(g.Get("seed"))
	p.FrequencyPenalty = translate.FloatPtr(g.Get("frequency_penalty"))
	p.PresencePenalty = translate.FloatPtr(g.Get("presence_penalty"))
	p.Stop = translate.StrList(g.Get("stop"))
	p.User = g.Get("user").String()
	if rf := g.Get("response_format"); rf.Exists() {
		p.ResponseFormat = json.RawMessage(rf.Raw)
	}
	if e := g.Get("reasoning_effort").String(); e != "" {
		req.Thinking = translate.EffortToThinking(e)
	}
	req.Extra = translate.ExtraOf(body, openAIKnown)
	return req, nil
}

func EffortToThinking(e string) translate.Thinking {
	switch strings.ToLower(e) {
	case "none", "off":
		return translate.Thinking{Mode: "none"}
	case "auto":
		return translate.Thinking{Mode: "auto"}
	}
	return translate.Thinking{Mode: "level", Level: strings.ToLower(e)}
}

func ReadOpenAIMessage(m gjson.Result) translate.Message {
	msg := translate.Message{Role: translate.Role(m.Get("role").String()), Name: m.Get("name").String(), ToolCallID: m.Get("tool_call_id").String()}
	// The developer role is a system turn to every upstream that does not know it.
	if msg.Role == "developer" {
		msg.Role = translate.RoleSystem
	}
	msg.Content = ReadOpenAIContent(m.Get("content"))
	// `reasoning` is the alias some providers stream; a non-string value (an
	// object of provider details) is not thinking text.
	reasoning := m.Get("reasoning_content").String()
	if reasoning == "" {
		if r := m.Get("reasoning"); r.Type == gjson.String {
			reasoning = r.String()
		}
	}
	msg.Reasoning = reasoning
	if msg.Role == translate.RoleTool && (m.Get("is_error").Bool() || m.Get("status").String() == "error") {
		msg.Error = true
	}
	for _, tc := range m.Get("tool_calls").Array() {
		msg.ToolCalls = append(msg.ToolCalls, translate.ToolCall{
			ID: tc.Get("id").String(), Name: tc.Get("function.name").String(),
			Arguments: translate.ArgsText(tc.Get("function.arguments")),
		})
	}
	if sig := m.Get("extra_content.google.thought_signature").String(); sig != "" && len(msg.ToolCalls) > 0 {
		msg.ToolCalls[0].Signature = sig
	}
	return msg
}

// ArgsText is the arguments of a call as JSON text: a string stays as it is, an
// object (some clients send one) is encoded.
func ArgsText(r gjson.Result) string {
	switch {
	case !r.Exists() || r.Type == gjson.Null:
		return ""
	case r.Type == gjson.String:
		return r.String()
	}
	return r.Raw
}

func ReadOpenAIContent(c gjson.Result) []translate.Part {
	switch {
	case c.Type == gjson.String:
		if c.String() == "" {
			return nil
		}
		return translate.TextPart(c.String())
	case !c.IsArray():
		return nil
	}
	var parts []translate.Part
	for _, p := range c.Array() {
		switch p.Get("type").String() {
		case "text", "input_text", "output_text":
			parts = append(parts, translate.Part{Type: translate.PartText, Text: p.Get("text").String()})
		case "image_url", "input_image":
			url := p.Get("image_url.url").String()
			if url == "" {
				url = p.Get("image_url").String()
			}
			parts = append(parts, translate.Part{Type: translate.PartImage, URL: url, Detail: p.Get("image_url.detail").String()})
		case "input_audio":
			parts = append(parts, translate.Part{Type: translate.PartAudio, Data: p.Get("input_audio.data").String(), MediaType: "audio/" + p.Get("input_audio.format").String()})
		case "file":
			parts = append(parts, translate.Part{Type: translate.PartFile, Data: p.Get("file.file_data").String(), Name: p.Get("file.filename").String(), URL: p.Get("file.file_id").String()})
		}
		// Every other type (thinking, tool_use, a foreign API's block) is dropped:
		// it is not OpenAI content and an upstream would reject it.
	}
	return parts
}

func ReadOpenAITool(t gjson.Result) translate.Tool {
	if t.Get("type").String() == "function" || t.Get("function").Exists() {
		return translate.Tool{
			Name: t.Get("function.name").String(), Description: t.Get("function.description").String(),
			Parameters: json.RawMessage(t.Get("function.parameters").Raw),
		}
	}
	return translate.Tool{Raw: json.RawMessage(t.Raw)}
}

func readOpenAIToolChoice(c gjson.Result) *translate.ToolChoice {
	switch {
	case !c.Exists() || c.Type == gjson.Null:
		return nil
	case c.Type == gjson.String:
		return &translate.ToolChoice{Mode: c.String()}
	case c.Get("function.name").Exists():
		return &translate.ToolChoice{Mode: "tool", Name: c.Get("function.name").String()}
	}
	return nil
}

// WriteRequest writes the hub as a Chat Completions body.
func (OpenAICodec) WriteRequest(r *translate.Request, c *translate.Ctx) ([]byte, error) {
	o := translate.Obj{"model": r.Model}
	msgs := make([]any, 0, len(r.Messages))
	for _, m := range r.Messages {
		if w, ok := WriteOpenAIMessage(m, c.Entry.Transport.Quirks.PreserveCacheControl); ok {
			translate.InjectReasoning(w, m, translate.ReasoningScope(c))
			msgs = append(msgs, w)
		}
	}
	o["messages"] = msgs
	if len(r.Tools) > 0 {
		tools := make([]any, 0, len(r.Tools))
		for _, t := range r.Tools {
			if len(t.Raw) > 0 {
				tools = append(tools, t.Raw)
				continue
			}
			params := t.Parameters
			if len(params) == 0 {
				params = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			fn := translate.Obj{"name": t.Name, "description": t.Description, "parameters": params}
			tools = append(tools, translate.Obj{"type": "function", "function": fn})
		}
		o["tools"] = tools
	}
	if tc := r.ToolChoice; tc != nil {
		if tc.Mode == "tool" {
			o["tool_choice"] = translate.Obj{"type": "function", "function": translate.Obj{"name": tc.Name}}
		} else {
			o["tool_choice"] = tc.Mode
		}
	}
	if r.Stream {
		o["stream"] = true
		o["stream_options"] = translate.Obj{"include_usage": true}
	}
	p := r.Params
	if p.MaxTokens != nil {
		o["max_tokens"] = *p.MaxTokens
	}
	if p.Temperature != nil {
		o["temperature"] = *p.Temperature
	}
	if p.TopP != nil {
		o["top_p"] = *p.TopP
	}
	if p.Seed != nil {
		o["seed"] = *p.Seed
	}
	if p.FrequencyPenalty != nil {
		o["frequency_penalty"] = *p.FrequencyPenalty
	}
	if p.PresencePenalty != nil {
		o["presence_penalty"] = *p.PresencePenalty
	}
	if len(p.Stop) > 0 {
		o["stop"] = p.Stop
	}
	if p.User != "" {
		o["user"] = p.User
	}
	if len(p.ResponseFormat) > 0 {
		o["response_format"] = p.ResponseFormat
	}
	if r.Thinking.Set() {
		WriteOpenAIThinking(o, r.Thinking, c)
	}
	return json.Marshal(translate.WithExtra(o, r.Extra))
}

// WriteOpenAIThinking writes a thinking request in the form the upstream reads.
// Most OpenAI-format upstreams take reasoning_effort. Nous takes a reasoning
// object with an on switch, and sends nothing to turn thinking off. Kimi's
// highest level is max, and a model that cannot switch off never gets "none".
func WriteOpenAIThinking(o translate.Obj, th translate.Thinking, c *translate.Ctx) {
	var caps upstream.Capability
	if c != nil {
		caps = c.Caps
	}
	none := th.Mode == "none"
	// A model that cannot switch thinking off is asked for the least instead.
	if none && !caps.ThinkingCanOff {
		th, none = translate.Thinking{Mode: "level", Level: "minimal"}, false
	}
	level := th.Level
	// A compatible node is a gateway that forwards an OpenAI-shaped thinking
	// object rather than an effort word: it keeps an explicit thinking budget,
	// and its effort is clamped to the levels it reads.
	if isCompatNode(c) {
		switch th.Mode {
		case "budget":
			if th.Budget > 0 {
				o["thinking"] = translate.Obj{"type": "enabled", "budget_tokens": th.Budget}
				return
			}
		case "auto":
			o["reasoning_effort"] = "high"
			return
		case "level":
			if effort := compatEffort(th.Level); effort != "" {
				o["reasoning_effort"] = effort
			}
			return
		}
	}
	switch th.Mode {
	case "budget":
		level = budgetLevel(th.Budget)
		if level == "" {
			level = "medium"
		}
	case "auto":
		level = "auto"
	case "none":
		level = ""
	}
	switch caps.ThinkingFormat {
	case "nous":
		// Disabled is the field left out; enabled is a nested object.
		if none {
			return
		}
		if level == "auto" {
			level = "medium"
		}
		out := translate.Obj{"enabled": true}
		if level != "" {
			out["effort"] = level
		}
		o["reasoning"] = out
		return
	case "zai":
		// Z.ai ignores a disabled thinking object; turning it off is its own switch.
		if none {
			o["enable_thinking"] = false
			return
		}
		o["thinking"] = translate.Obj{"type": "enabled"}
		if caps.ThinkingEffortSupported {
			o["reasoning_effort"] = zaiLevel(level)
		}
		return
	case "kimi":
		if none {
			o["thinking"] = translate.Obj{"type": "disabled"}
			return
		}
		if effort := kimiLevel(level); effort != "" {
			o["reasoning_effort"] = effort
		}
		return
	case "tokenrouter":
		// The gateway reads its own levels and refuses "none" and "auto", so a
		// request in either shape carries no field at all.
		if none || level == "auto" || level == "" {
			return
		}
		o["reasoning_effort"] = level
		return
	}
	switch th.Mode {
	case "none":
		o["reasoning_effort"] = "none"
	case "level":
		o["reasoning_effort"] = th.Level
	}
}

// isCompatNode reports a user node of the OpenAI-compatible kind, which is the
// gateway the reference clamps an effort for.
func isCompatNode(c *translate.Ctx) bool {
	return c != nil && strings.HasPrefix(c.Provider, upstream.NodeTypeOpenAI)
}

// compatEffort maps a level to what a compatible node reads, or "" for a value
// it does not know.
func compatEffort(level string) string {
	switch level {
	case "auto":
		return "high"
	case "minimal":
		return "low"
	case "off", "low", "medium", "high", "xhigh", "max":
		return level
	}
	return ""
}

// zaiLevel maps a level to the three Z.ai reads: low, high and max.
func zaiLevel(l string) string {
	switch l {
	case "low", "minimal":
		return "low"
	case "medium", "high":
		return "high"
	}
	return "max"
}

// kimiLevel maps a level to one Kimi reads, or "" for one it does not.
func kimiLevel(l string) string {
	switch l {
	case "auto":
		return "high"
	case "minimal":
		return "low"
	case "xhigh", "ultra":
		return "max"
	case "low", "medium", "high", "max":
		return l
	}
	return ""
}

// budgetLevel is the level a token budget stands for, or "" for none.
func budgetLevel(b int) string {
	switch {
	case b <= 0:
		return ""
	case b <= 768:
		return "minimal"
	case b <= 4096:
		return "low"
	case b <= 16384:
		return "medium"
	case b <= 28672:
		return "high"
	case b <= 80384:
		return "xhigh"
	}
	return "max"
}

func WriteOpenAIMessage(m translate.Message, keepCache bool) (translate.Obj, bool) {
	// A message with no content, no tool call and no tool result carries nothing.
	text := m.Text()
	if m.Role != translate.RoleTool && len(m.ToolCalls) == 0 && strings.TrimSpace(text) == "" && !hasNonText(m.Content) {
		return nil, false
	}
	w := translate.Obj{"role": string(m.Role)}
	switch {
	case onlyText(m.Content) && !(keepCache && hasCache(m.Content)):
		w["content"] = text
	default:
		w["content"] = openAIContent(m.Content, keepCache)
	}
	if m.Name != "" {
		w["name"] = m.Name
	}
	if m.ToolCallID != "" {
		w["tool_call_id"] = m.ToolCallID
	}
	if len(m.ToolCalls) > 0 {
		calls := make([]any, 0, len(m.ToolCalls))
		for _, tc := range m.ToolCalls {
			call := translate.Obj{"id": tc.ID, "type": "function", "function": translate.Obj{"name": tc.Name, "arguments": tc.Arguments}}
			if tc.Signature != "" {
				call["extra_content"] = translate.Obj{"google": translate.Obj{"thought_signature": tc.Signature}}
			}
			calls = append(calls, call)
		}
		w["tool_calls"] = calls
		if text == "" {
			w["content"] = nil
		}
	}
	if m.Reasoning != "" {
		w["reasoning_content"] = m.Reasoning
	}
	return w, true
}

func hasNonText(parts []translate.Part) bool {
	for _, p := range parts {
		if p.Type != translate.PartText {
			return true
		}
	}
	return false
}

func onlyText(parts []translate.Part) bool { return !hasNonText(parts) }

func hasCache(parts []translate.Part) bool {
	for _, p := range parts {
		if len(p.Cache) > 0 {
			return true
		}
	}
	return false
}

func openAIContent(parts []translate.Part, keepCache bool) []any {
	out := make([]any, 0, len(parts))
	for _, p := range parts {
		switch p.Type {
		case translate.PartText:
			t := translate.Obj{"type": "text", "text": p.Text}
			if keepCache && len(p.Cache) > 0 {
				t["cache_control"] = p.Cache
			}
			out = append(out, t)
		case translate.PartImage:
			u := p.URL
			if u == "" && p.Data != "" {
				u = "data:" + p.MediaType + ";base64," + p.Data
			}
			img := translate.Obj{"url": u}
			if p.Detail != "" {
				img["detail"] = p.Detail
			}
			out = append(out, translate.Obj{"type": "image_url", "image_url": img})
		case translate.PartAudio:
			out = append(out, translate.Obj{"type": "input_audio", "input_audio": translate.Obj{"data": p.Data, "format": strings.TrimPrefix(p.MediaType, "audio/")}})
		case translate.PartFile:
			f := translate.Obj{}
			if p.Data != "" {
				f["file_data"] = p.Data
			}
			if p.Name != "" {
				f["filename"] = p.Name
			}
			if p.URL != "" {
				f["file_id"] = p.URL
			}
			out = append(out, translate.Obj{"type": "file", "file": f})
		}
	}
	return out
}

// ReadResponse reads a Chat Completions answer.
func (OpenAICodec) ReadResponse(body []byte, c *translate.Ctx) (*translate.Response, error) {
	if !translate.ValidObject(body) {
		return nil, translate.ErrBadBody
	}
	g := gjson.ParseBytes(body)
	resp := &translate.Response{ID: g.Get("id").String(), Model: g.Get("model").String(), Created: g.Get("created").Int()}
	ch := g.Get("choices.0")
	resp.Message = ReadOpenAIMessage(ch.Get("message"))
	if resp.Message.Role == "" {
		resp.Message.Role = translate.RoleAssistant
	}
	resp.FinishReason = ch.Get("finish_reason").String()
	resp.Usage = translate.ReadOpenAIUsage(g.Get("usage"))
	resp.Extra = translate.ExtraOf(body, translate.Set("id", "object", "created", "model", "choices", "usage"))
	return resp, nil
}

func ReadOpenAIUsage(u gjson.Result) translate.Usage {
	if !u.Exists() {
		return translate.Usage{}
	}
	out := translate.Usage{
		Prompt: u.Get("prompt_tokens").Int(), Completion: u.Get("completion_tokens").Int(),
		Reasoning:  u.Get("completion_tokens_details.reasoning_tokens").Int(),
		Cached:     firstPositive(u.Get("prompt_tokens_details.cached_tokens").Int(), u.Get("prompt_cache_hit_tokens").Int()),
		CacheWrite: u.Get("prompt_tokens_details.cache_creation_tokens").Int(),
	}
	return translate.NormalizeUsage(out)
}

func firstPositive(vals ...int64) int64 {
	for _, v := range vals {
		if v > 0 {
			return v
		}
	}
	return 0
}

// WriteResponse writes the hub answer as a Chat Completions body.
func (OpenAICodec) WriteResponse(r *translate.Response, c *translate.Ctx) ([]byte, error) {
	msg, _ := WriteOpenAIMessage(r.Message, false)
	if msg == nil {
		msg = translate.Obj{"role": "assistant", "content": ""}
	}
	msg["role"] = "assistant"
	finish := r.FinishReason
	if finish == "" {
		finish = translate.FinishStop
	}
	o := translate.Obj{
		"id": r.ID, "object": "chat.completion", "created": r.Created, "model": r.Model,
		"choices": []any{translate.Obj{"index": 0, "message": msg, "finish_reason": finish}},
		"usage":   openAIUsage(r.Usage),
	}
	return json.Marshal(translate.WithExtra(o, r.Extra))
}

func openAIUsage(u translate.Usage) translate.Obj {
	out := translate.Obj{"prompt_tokens": u.Prompt, "completion_tokens": u.Completion, "total_tokens": u.Prompt + u.Completion}
	if u.Cached > 0 || u.CacheWrite > 0 {
		d := translate.Obj{}
		if u.Cached > 0 {
			d["cached_tokens"] = u.Cached
		}
		if u.CacheWrite > 0 {
			d["cache_creation_tokens"] = u.CacheWrite
		}
		out["prompt_tokens_details"] = d
	}
	if u.Reasoning > 0 {
		out["completion_tokens_details"] = translate.Obj{"reasoning_tokens": u.Reasoning}
	}
	return out
}
