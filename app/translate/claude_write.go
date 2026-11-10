package translate

import (
	"encoding/json"
	"strings"
)

// cacheMarker is a prompt-cache marker on a block. TTL is empty for the
// five-minute default.
type cacheMarker struct {
	Type string `json:"type"`
	TTL  string `json:"ttl,omitempty"`
}

func fiveMinutes() *cacheMarker { return &cacheMarker{Type: "ephemeral"} }
func oneHour() *cacheMarker     { return &cacheMarker{Type: "ephemeral", TTL: "1h"} }

// maxCacheMarkers is how many markers Anthropic accepts in one request.
const maxCacheMarkers = 4

// claudeBlock is one content block of a Messages request.
type claudeBlock struct {
	Type         string          `json:"type"`
	Text         string          `json:"text,omitempty"`
	Source       *claudeSource   `json:"source,omitempty"`
	ID           string          `json:"id,omitempty"`
	Name         string          `json:"name,omitempty"`
	Input        json.RawMessage `json:"input,omitempty"`
	ToolUseID    string          `json:"tool_use_id,omitempty"`
	Content      any             `json:"content,omitempty"`
	Thinking     string          `json:"thinking,omitempty"`
	Signature    string          `json:"signature,omitempty"`
	CacheControl *cacheMarker    `json:"cache_control,omitempty"`
}

type claudeSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
}

type claudeMessage struct {
	Role    string        `json:"role"`
	Content []claudeBlock `json:"content"`
}

// WriteRequest writes the hub as a Messages body, prepared for an Anthropic
// upstream: client cache markers are gone, at most four of ours are set, the
// history is repaired and a thinking block is signed or dropped.
func (ClaudeCodec) WriteRequest(r *Request, c *Ctx) ([]byte, error) {
	system, rest := splitSystem(r.Messages)
	msgs := claudeMessages(rest, c)
	msgs = repairClaudeHistory(msgs)

	var tools []claudeTool
	for _, t := range r.Tools {
		if len(t.Raw) > 0 {
			tools = append(tools, claudeTool{raw: t.Raw})
			continue
		}
		schema := t.Parameters
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		tools = append(tools, claudeTool{Name: t.Name, Description: t.Description, InputSchema: schema})
	}
	if c.Entry.Transport.Quirks.RequireClaudeToolType {
		tools = typeTools(tools)
	}
	// Outside Anthropic, only the tool types the provider lists are sent.
	if len(c.Caps.ClaudeToolTypes) > 0 {
		tools = filterToolTypes(tools, c.Caps.ClaudeToolTypes)
	}

	sys := claudeSystem(system, r.Params.ResponseFormat)
	markCaches(sys, tools, msgs)

	o := obj{"model": r.Model, "messages": msgsToAny(msgs)}
	if r.Params.MaxTokens != nil {
		o["max_tokens"] = *r.Params.MaxTokens
	} else {
		o["max_tokens"] = defaultClaudeMaxTokens(c)
	}
	if len(sys) > 0 {
		o["system"] = sys
	}
	if len(tools) > 0 {
		o["tools"] = tools
	}
	if tc := r.ToolChoice; tc != nil {
		if v := claudeToolChoice(tc, c, r.Thinking); v != nil {
			o["tool_choice"] = v
		}
	}
	if r.Stream {
		o["stream"] = true
	}
	p := r.Params
	if p.Temperature != nil {
		o["temperature"] = *p.Temperature
	}
	if p.TopP != nil {
		o["top_p"] = *p.TopP
	}
	if p.TopK != nil {
		o["top_k"] = *p.TopK
	}
	if len(p.Stop) > 0 {
		o["stop_sequences"] = p.Stop
	}
	if p.User != "" {
		o["metadata"] = obj{"user_id": p.User}
	}
	applyClaudeThinking(o, r, c)
	extra := map[string]json.RawMessage{}
	for k, v := range r.Extra {
		extra[k] = v
	}
	if c.Caps.DropOutputConfig {
		delete(extra, "output_config")
	}
	if !c.Caps.KeepContextMgmt {
		delete(extra, "context_management")
	}
	return json.Marshal(withExtra(o, extra))
}

func defaultClaudeMaxTokens(c *Ctx) int {
	if c.Caps.MaxOutput > 0 {
		return min(c.Caps.MaxOutput, 64000)
	}
	return 64000
}

type claudeTool struct {
	Type         string          `json:"type,omitempty"`
	Name         string          `json:"name,omitempty"`
	Description  string          `json:"description,omitempty"`
	InputSchema  json.RawMessage `json:"input_schema,omitempty"`
	CacheControl *cacheMarker    `json:"cache_control,omitempty"`
	raw          json.RawMessage
}

func (t claudeTool) MarshalJSON() ([]byte, error) {
	if len(t.raw) > 0 {
		var m map[string]json.RawMessage
		if json.Unmarshal(t.raw, &m) == nil {
			// A client marker never survives.
			delete(m, "cache_control")
			if t.CacheControl != nil {
				b, _ := json.Marshal(t.CacheControl)
				m["cache_control"] = b
			}
			return json.Marshal(sortedRaw(m))
		}
		return t.raw, nil
	}
	type plain claudeTool
	return json.Marshal(plain(t))
}

// toolHasDeferLoading reports a tool that declares deferred loading, which
// Anthropic refuses together with a cache marker.
func (t claudeTool) deferLoading() bool {
	if len(t.raw) == 0 {
		return false
	}
	var m struct {
		Defer bool `json:"defer_loading"`
	}
	return json.Unmarshal(t.raw, &m) == nil && m.Defer
}

func (t claudeTool) toolType() string {
	if len(t.raw) == 0 {
		return "custom"
	}
	var m struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(t.raw, &m)
	if m.Type == "" {
		return "custom"
	}
	return m.Type
}

func filterToolTypes(tools []claudeTool, allowed []string) []claudeTool {
	ok := make(map[string]bool, len(allowed))
	for _, a := range allowed {
		ok[a] = true
	}
	out := tools[:0:0]
	for _, t := range tools {
		if ok[t.toolType()] {
			out = append(out, t)
		}
	}
	return out
}

// typeTools gives every tool that has no type the type "custom", for gateways
// that refuse a typeless tool.
func typeTools(tools []claudeTool) []claudeTool {
	for i, t := range tools {
		if len(t.raw) == 0 {
			tools[i].Type = "custom"
		}
	}
	return tools
}

func splitSystem(msgs []Message) (system []string, rest []Message) {
	for _, m := range msgs {
		if m.Role == RoleSystem {
			if t := strings.TrimSpace(m.Text()); t != "" {
				system = append(system, m.Text())
			}
			continue
		}
		rest = append(rest, m)
	}
	return system, rest
}

func claudeSystem(parts []string, responseFormat json.RawMessage) []claudeBlock {
	parts = append([]string{}, parts...)
	if len(responseFormat) > 0 {
		var rf struct {
			Type       string `json:"type"`
			JSONSchema struct {
				Schema json.RawMessage `json:"schema"`
			} `json:"json_schema"`
		}
		if json.Unmarshal(responseFormat, &rf) == nil {
			switch {
			case rf.Type == "json_schema" && len(rf.JSONSchema.Schema) > 0:
				parts = append(parts, "You must respond with valid JSON that strictly follows this JSON schema:\n```json\n"+string(rf.JSONSchema.Schema)+"\n```\nRespond ONLY with the JSON object, no other text.")
			case rf.Type == "json_object":
				parts = append(parts, "You must respond with valid JSON. Respond ONLY with a JSON object, no other text.")
			}
		}
	}
	if len(parts) == 0 {
		return nil
	}
	return []claudeBlock{{Type: "text", Text: strings.Join(parts, "\n")}}
}

func claudeToolChoice(tc *ToolChoice, c *Ctx, th Thinking) any {
	// An upstream that thinks refuses a forced tool choice; it is relaxed to auto.
	forced := tc.Mode == "required" || tc.Mode == "tool"
	if forced && c.Caps.RelaxToolChoice && th.Set() && th.Mode != "none" {
		return obj{"type": "auto"}
	}
	switch tc.Mode {
	case "auto":
		return obj{"type": "auto"}
	case "required":
		return obj{"type": "any"}
	case "none":
		return obj{"type": "none"}
	case "tool":
		return obj{"type": "tool", "name": tc.Name}
	}
	return nil
}

func applyClaudeThinking(o obj, r *Request, c *Ctx) {
	th := r.Thinking
	if !th.Set() {
		return
	}
	switch th.Mode {
	case "none":
		// A rejected "disabled" type is replaced by the model's own off type.
		typ := "disabled"
		if c.Caps.ThinkingOffType != "" {
			typ = c.Caps.ThinkingOffType
		}
		o["thinking"] = obj{"type": typ}
	case "budget":
		o["thinking"] = obj{"type": "enabled", "budget_tokens": th.Budget}
	case "level":
		if c.Caps.ThinkingFormat == "claude-adaptive" {
			o["thinking"] = obj{"type": "adaptive"}
			o["output_config"] = obj{"effort": th.Level}
		} else {
			o["thinking"] = obj{"type": "enabled", "budget_tokens": c.Budget(th.Level)}
		}
	case "auto":
		o["thinking"] = obj{"type": "adaptive"}
	}
	if t, ok := o["thinking"].(obj); ok && th.Display != "" && t["type"] != "disabled" {
		t["display"] = th.Display
	}
}

// claudeMessages turns hub turns into Claude turns. A tool result is its own
// user message directly after the call; other content of one role is merged.
func claudeMessages(msgs []Message, c *Ctx) []claudeMessage {
	var out []claudeMessage
	var role string
	var cur []claudeBlock
	flush := func() {
		if role != "" && len(cur) > 0 {
			out = append(out, claudeMessage{Role: role, Content: cur})
		}
		cur = nil
	}
	for _, m := range msgs {
		newRole := "assistant"
		if m.Role == RoleUser || m.Role == RoleTool {
			newRole = "user"
		}
		blocks := claudeBlocks(m, c)
		if m.Role == RoleTool {
			flush()
			role = ""
			out = append(out, claudeMessage{Role: "user", Content: blocks})
			continue
		}
		if role != newRole {
			flush()
			role = newRole
		}
		cur = append(cur, blocks...)
		if len(m.ToolCalls) > 0 {
			flush()
			role = ""
		}
	}
	flush()
	return out
}

func claudeBlocks(m Message, c *Ctx) []claudeBlock {
	if m.Role == RoleTool {
		content := m.Text()
		var inner []claudeBlock
		for _, p := range m.Content {
			if p.Type == PartImage {
				inner = append(inner, claudeBlock{Type: "image", Source: imageSource(p)})
			}
		}
		b := claudeBlock{Type: "tool_result", ToolUseID: m.ToolCallID}
		if len(inner) > 0 {
			blocks := []any{}
			if content != "" {
				blocks = append(blocks, claudeBlock{Type: "text", Text: content})
			}
			for _, i := range inner {
				blocks = append(blocks, i)
			}
			b.Content = blocks
		} else {
			b.Content = content
		}
		return []claudeBlock{b}
	}
	var blocks []claudeBlock
	if m.Role == RoleAssistant && m.Reasoning != "" {
		blocks = append(blocks, claudeBlock{Type: "thinking", Thinking: m.Reasoning, Signature: m.ReasoningSignature})
	}
	for _, p := range m.Content {
		switch p.Type {
		case PartText:
			if strings.TrimSpace(p.Text) != "" {
				blocks = append(blocks, claudeBlock{Type: "text", Text: p.Text})
			}
		case PartImage:
			if src := imageSource(p); src != nil {
				blocks = append(blocks, claudeBlock{Type: "image", Source: src})
			}
		case PartFile:
			if p.Data != "" {
				blocks = append(blocks, claudeBlock{Type: "document", Source: &claudeSource{Type: "base64", MediaType: p.MediaType, Data: p.Data}})
			}
		}
	}
	for _, tc := range m.ToolCalls {
		input := json.RawMessage(tc.Arguments)
		if !json.Valid(input) || len(input) == 0 {
			input = json.RawMessage("{}")
		}
		blocks = append(blocks, claudeBlock{Type: "tool_use", ID: tc.ID, Name: tc.Name, Input: input})
	}
	return blocks
}

// imageSource is an image as Claude reads it: inline base64 or a URL. A data
// URI is split into its media type and data.
func imageSource(p Part) *claudeSource {
	if p.Data != "" {
		return &claudeSource{Type: "base64", MediaType: p.MediaType, Data: p.Data}
	}
	if media, data, ok := splitDataURI(p.URL); ok {
		return &claudeSource{Type: "base64", MediaType: media, Data: data}
	}
	if strings.HasPrefix(p.URL, "data:") {
		return nil
	}
	if p.URL != "" {
		return &claudeSource{Type: "url", URL: p.URL}
	}
	return nil
}

func msgsToAny(msgs []claudeMessage) []any {
	out := make([]any, len(msgs))
	for i, m := range msgs {
		out[i] = m
	}
	return out
}

// markCaches sets prompt-cache markers. The last system block and the last
// cacheable tool get one hour, the last assistant block five minutes, and the
// final tool results of an agent loop five minutes while fewer than four are
// set. A conversation with no assistant turn yet marks its last message. Client
// markers were never read, so at most four are ever sent and an unchanged
// prefix stays byte-identical from one turn to the next.
func markCaches(system []claudeBlock, tools []claudeTool, msgs []claudeMessage) {
	n := 0
	if len(system) > 0 {
		system[len(system)-1].CacheControl = oneHour()
		n++
	}
	for i := len(tools) - 1; i >= 0; i-- {
		if tools[i].deferLoading() {
			continue
		}
		tools[i].CacheControl = oneHour()
		n++
		break
	}
	markLastAssistant := func() bool {
		for i := len(msgs) - 1; i >= 0; i-- {
			if msgs[i].Role != "assistant" {
				continue
			}
			for j := len(msgs[i].Content) - 1; j >= 0; j-- {
				switch msgs[i].Content[j].Type {
				case "text", "tool_use", "tool_result", "image":
					msgs[i].Content[j].CacheControl = fiveMinutes()
					return true
				}
			}
		}
		return false
	}
	if markLastAssistant() {
		n++
	} else if len(msgs) > 0 {
		last := &msgs[len(msgs)-1]
		for j := len(last.Content) - 1; j >= 0; j-- {
			if t := last.Content[j].Type; t == "text" || t == "tool_result" || t == "image" {
				last.Content[j].CacheControl = fiveMinutes()
				n++
				break
			}
		}
	}
	// The results at the end of an agent loop are what the next turn extends.
	if len(msgs) == 0 || msgs[len(msgs)-1].Role != "user" {
		return
	}
	last := msgs[len(msgs)-1].Content
	for j := len(last) - 1; j >= 0 && n < maxCacheMarkers; j-- {
		if last[j].Type == "tool_result" && last[j].CacheControl == nil {
			last[j].CacheControl = fiveMinutes()
			n++
		}
	}
}

// repairClaudeHistory fixes what Anthropic rejects: turns that merely repeat a
// role are merged with tool results first, and the conversation must end on a
// user turn. A thinking block is kept only with a signature, so an unsigned one
// is dropped. Empty messages go.
func repairClaudeHistory(msgs []claudeMessage) []claudeMessage {
	var out []claudeMessage
	for _, m := range msgs {
		blocks := m.Content[:0:0]
		for _, b := range m.Content {
			if b.Type == "thinking" && (b.Signature == "" || strings.TrimSpace(b.Thinking) == "") {
				continue
			}
			blocks = append(blocks, b)
		}
		if len(blocks) == 0 {
			continue
		}
		// Text after a tool_use in one assistant turn is not accepted.
		blocks = textBeforeToolUse(blocks)
		m.Content = blocks
		if n := len(out); n > 0 && out[n-1].Role == m.Role {
			out[n-1].Content = mergeBlocks(out[n-1].Content, m.Content)
			continue
		}
		out = append(out, m)
	}
	// A trailing assistant turn is a prefill and stays. A conversation must not
	// open on an assistant turn though.
	if len(out) > 0 && out[0].Role != "user" {
		out = append([]claudeMessage{{Role: "user", Content: []claudeBlock{{Type: "text", Text: "..."}}}}, out...)
	}
	return out
}

// textBeforeToolUse drops text that follows a tool_use in an assistant turn.
func textBeforeToolUse(blocks []claudeBlock) []claudeBlock {
	seen := false
	out := blocks[:0:0]
	for _, b := range blocks {
		if seen && b.Type == "text" {
			continue
		}
		if b.Type == "tool_use" {
			seen = true
		}
		out = append(out, b)
	}
	return out
}

// mergeBlocks joins two turns of one role, tool results first.
func mergeBlocks(a, b []claudeBlock) []claudeBlock {
	all := append(append([]claudeBlock{}, a...), b...)
	var results, rest []claudeBlock
	for _, x := range all {
		if x.Type == "tool_result" {
			results = append(results, x)
		} else {
			rest = append(rest, x)
		}
	}
	return append(results, rest...)
}

// WriteResponse writes the hub answer as a Messages body.
func (ClaudeCodec) WriteResponse(r *Response, c *Ctx) ([]byte, error) {
	var blocks []any
	if r.Message.Reasoning != "" {
		blocks = append(blocks, obj{"type": "thinking", "thinking": r.Message.Reasoning, "signature": r.Message.ReasoningSignature})
	}
	for _, p := range r.Message.Content {
		if p.Type == PartText {
			blocks = append(blocks, obj{"type": "text", "text": p.Text})
		}
	}
	for _, tc := range r.Message.ToolCalls {
		input := json.RawMessage(tc.Arguments)
		if !json.Valid(input) || len(input) == 0 {
			input = json.RawMessage("{}")
		}
		blocks = append(blocks, obj{"type": "tool_use", "id": tc.ID, "name": tc.Name, "input": input})
	}
	if len(blocks) == 0 {
		blocks = []any{obj{"type": "text", "text": ""}}
	}
	usage := obj{
		"input_tokens":  r.Usage.Prompt - r.Usage.Cached - r.Usage.CacheWrite,
		"output_tokens": r.Usage.Completion,
	}
	if r.Usage.Cached > 0 {
		usage["cache_read_input_tokens"] = r.Usage.Cached
	}
	if r.Usage.CacheWrite > 0 {
		usage["cache_creation_input_tokens"] = r.Usage.CacheWrite
	}
	o := obj{
		"id": r.ID, "type": "message", "role": "assistant", "model": r.Model, "content": blocks,
		"stop_reason": FinishToClaude(r.FinishReason), "stop_sequence": nil, "usage": usage,
	}
	return json.Marshal(withExtra(o, r.Extra))
}
