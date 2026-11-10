package translate

import (
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
)

// ClaudeCodec joins the Anthropic Messages API to the hub.
type ClaudeCodec struct{}

func init() { Register(ClaudeCodec{}) }

// Name is the format name.
func (ClaudeCodec) Name() string { return FormatClaude }

var claudeKnown = set("model", "messages", "system", "tools", "tool_choice", "stream", "max_tokens", "temperature",
	"top_p", "top_k", "stop_sequences", "metadata", "thinking")

// billingHeader is a first system line Claude Code adds for the upstream's own
// accounting; it is not part of the conversation and is removed on the way in.
const billingHeader = "x-anthropic-billing-header:"

// ReadRequest reads a Messages body.
func (ClaudeCodec) ReadRequest(body []byte, c *Ctx) (*Request, error) {
	if !validObject(body) {
		return nil, ErrBadBody
	}
	g := gjson.ParseBytes(body)
	req := &Request{Model: g.Get("model").String(), Stream: g.Get("stream").Bool()}

	if sys := claudeSystemText(g.Get("system")); sys != "" {
		req.Messages = append(req.Messages, Message{Role: RoleSystem, Content: TextPart(sys)})
	}
	msgs := g.Get("messages")
	if msgs.Exists() && !msgs.IsArray() {
		return nil, ErrBadBody
	}
	for _, m := range msgs.Array() {
		if !m.IsObject() {
			return nil, ErrBadBody
		}
		req.Messages = append(req.Messages, readClaudeMessage(m)...)
	}
	for _, t := range g.Get("tools").Array() {
		req.Tools = append(req.Tools, readClaudeTool(t))
	}
	req.ToolChoice = readClaudeToolChoice(g.Get("tool_choice"))

	p := &req.Params
	p.MaxTokens = intPtr(g.Get("max_tokens"))
	p.Temperature = floatPtr(g.Get("temperature"))
	p.TopP = floatPtr(g.Get("top_p"))
	p.TopK = intPtr(g.Get("top_k"))
	p.Stop = strList(g.Get("stop_sequences"))
	p.User = g.Get("metadata.user_id").String()

	switch g.Get("thinking.type").String() {
	case "enabled":
		if b := g.Get("thinking.budget_tokens"); b.Exists() {
			req.Thinking = Thinking{Mode: "budget", Budget: int(b.Int())}
		} else {
			req.Thinking = Thinking{Mode: "auto"}
		}
	case "adaptive":
		req.Thinking = Thinking{Mode: "auto"}
		if e := g.Get("output_config.effort").String(); e != "" {
			req.Thinking = Thinking{Mode: "level", Level: e}
		}
	case "disabled":
		req.Thinking = Thinking{Mode: "none"}
	}
	if d := g.Get("thinking.display").String(); d != "" && req.Thinking.Set() {
		req.Thinking.Display = d
	}
	req.Extra = extraOf(body, claudeKnown)
	return req, nil
}

// claudeSystemText flattens the system field, dropping the billing header line.
func claudeSystemText(s gjson.Result) string {
	switch {
	case s.Type == gjson.String:
		return stripBilling(s.String())
	case s.IsArray():
		var parts []string
		for _, b := range s.Array() {
			if t := stripBilling(b.Get("text").String()); t != "" {
				parts = append(parts, t)
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

func stripBilling(s string) string {
	if !strings.HasPrefix(strings.ToLower(s), billingHeader) {
		return s
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimPrefix(s[i+1:], "\r")
	}
	return ""
}

func readClaudeMessage(m gjson.Result) []Message {
	role := m.Get("role").String()
	content := m.Get("content")
	// A bare block object is a one-block array.
	if content.IsObject() {
		content = gjson.Parse("[" + content.Raw + "]")
	}
	// A system turn in the middle of a conversation is an instruction, which
	// Anthropic only accepts as a user turn.
	if role == "system" {
		if t := claudeBlocksText(content); strings.TrimSpace(t) != "" {
			return []Message{{Role: RoleUser, Content: TextPart("<instructions>\n" + t + "\n</instructions>")}}
		}
		return nil
	}
	hubRole := RoleAssistant
	if role == "user" || role == "tool" {
		hubRole = RoleUser
	}
	if content.Type == gjson.String {
		return []Message{{Role: hubRole, Content: TextPart(content.String())}}
	}
	if !content.IsArray() {
		return nil
	}

	var parts []Part
	var calls []ToolCall
	var results []Message
	var reasoning, reasoningSig string
	for _, b := range content.Array() {
		switch b.Get("type").String() {
		case "text":
			part := Part{Type: PartText, Text: b.Get("text").String()}
			if cc := b.Get("cache_control"); cc.IsObject() {
				part.Cache = json.RawMessage(cc.Raw)
			}
			parts = append(parts, part)
		case "image":
			if b.Get("source.type").String() == "base64" {
				parts = append(parts, Part{Type: PartImage, MediaType: b.Get("source.media_type").String(), Data: b.Get("source.data").String()})
			} else if u := b.Get("source.url").String(); u != "" {
				parts = append(parts, Part{Type: PartImage, URL: u})
			}
		case "document":
			parts = append(parts, Part{Type: PartFile, MediaType: b.Get("source.media_type").String(), Data: b.Get("source.data").String()})
		case "thinking":
			reasoning += b.Get("thinking").String()
			if s := b.Get("signature").String(); s != "" {
				reasoningSig = s
			}
		case "tool_use":
			args := b.Get("input").Raw
			if args == "" || args == "null" {
				args = "{}"
			}
			calls = append(calls, ToolCall{ID: b.Get("id").String(), Name: b.Get("name").String(), Arguments: args})
		case "tool_result":
			text, images := claudeResultContent(b.Get("content"))
			results = append(results, Message{Role: RoleTool, ToolCallID: b.Get("tool_use_id").String(), Content: TextPart(text)})
			if len(images) > 0 {
				// The tool role is text only, so an image a tool returned would vanish.
				// It goes in the user turn that follows, tagged with its call.
				parts = append(parts, Part{Type: PartText, Text: "[Image from tool result " + b.Get("tool_use_id").String() + "]"})
				parts = append(parts, images...)
			}
		}
	}

	switch {
	case len(results) > 0:
		out := results
		if len(parts) > 0 {
			out = append(out, Message{Role: RoleUser, Content: parts})
		}
		return out
	case len(calls) > 0:
		return []Message{{Role: RoleAssistant, Content: parts, ToolCalls: calls, Reasoning: reasoning, ReasoningSignature: reasoningSig}}
	case len(parts) > 0 || reasoning != "":
		return []Message{{Role: hubRole, Content: parts, Reasoning: reasoning, ReasoningSignature: reasoningSig}}
	case len(content.Array()) == 0:
		return []Message{{Role: hubRole}}
	}
	return nil
}

func claudeBlocksText(content gjson.Result) string {
	if content.Type == gjson.String {
		return content.String()
	}
	var parts []string
	for _, b := range content.Array() {
		if b.Get("type").String() == "text" {
			if t := b.Get("text").String(); t != "" {
				parts = append(parts, t)
			}
		}
	}
	return strings.Join(parts, "\n")
}

// claudeResultContent reads a tool result: its text, and any images it returned.
func claudeResultContent(c gjson.Result) (string, []Part) {
	switch {
	case c.Type == gjson.String:
		return c.String(), nil
	case c.IsArray():
		var texts []string
		var images []Part
		for _, b := range c.Array() {
			switch b.Get("type").String() {
			case "text":
				texts = append(texts, b.Get("text").String())
			case "image":
				if b.Get("source.type").String() == "base64" {
					images = append(images, Part{Type: PartImage, MediaType: b.Get("source.media_type").String(), Data: b.Get("source.data").String()})
				}
			}
		}
		if len(texts) == 0 && len(images) == 0 {
			return c.Raw, nil
		}
		return strings.Join(texts, "\n"), images
	case c.Exists() && c.Type != gjson.Null:
		return c.Raw, nil
	}
	return "", nil
}

func readClaudeTool(t gjson.Result) Tool {
	// A tool with a type other than "custom" is a server tool (web search, code
	// execution) the hub cannot model; it is passed on to a Claude upstream only.
	if typ := t.Get("type").String(); typ != "" && typ != "custom" {
		return Tool{Raw: json.RawMessage(t.Raw)}
	}
	params := json.RawMessage(t.Get("input_schema").Raw)
	return Tool{Name: t.Get("name").String(), Description: t.Get("description").String(), Parameters: params}
}

func readClaudeToolChoice(c gjson.Result) *ToolChoice {
	switch c.Get("type").String() {
	case "auto":
		return &ToolChoice{Mode: "auto"}
	case "any":
		return &ToolChoice{Mode: "required"}
	case "none":
		return &ToolChoice{Mode: "none"}
	case "tool":
		return &ToolChoice{Mode: "tool", Name: c.Get("name").String()}
	}
	return nil
}

// ReadResponse reads a Messages answer.
func (ClaudeCodec) ReadResponse(body []byte, c *Ctx) (*Response, error) {
	if !validObject(body) {
		return nil, ErrBadBody
	}
	g := gjson.ParseBytes(body)
	resp := &Response{ID: g.Get("id").String(), Model: g.Get("model").String()}
	msg := Message{Role: RoleAssistant}
	for _, b := range g.Get("content").Array() {
		switch b.Get("type").String() {
		case "text":
			msg.Content = append(msg.Content, Part{Type: PartText, Text: b.Get("text").String()})
		case "thinking":
			msg.Reasoning += b.Get("thinking").String()
			if s := b.Get("signature").String(); s != "" {
				msg.ReasoningSignature = s
			}
		case "tool_use":
			args := b.Get("input").Raw
			if args == "" {
				args = "{}"
			}
			msg.ToolCalls = append(msg.ToolCalls, ToolCall{ID: b.Get("id").String(), Name: c.RestoreTool(b.Get("name").String()), Arguments: args})
		}
	}
	resp.Message = msg
	resp.FinishReason = FinishWithCalls(FinishFromClaude(g.Get("stop_reason").String()), len(msg.ToolCalls))
	resp.Usage = readClaudeUsage(g.Get("usage"))
	resp.Extra = extraOf(body, set("id", "type", "role", "model", "content", "stop_reason", "stop_sequence", "usage"))
	return resp, nil
}

// readClaudeUsage reads Claude's usage, whose input count excludes the cache:
// the prompt is the input plus what was read from and written to the cache.
func readClaudeUsage(u gjson.Result) Usage {
	if !u.Exists() {
		return Usage{}
	}
	cached := u.Get("cache_read_input_tokens").Int()
	write := u.Get("cache_creation_input_tokens").Int()
	return NormalizeUsage(Usage{
		Prompt:     u.Get("input_tokens").Int() + cached + write,
		Completion: u.Get("output_tokens").Int(),
		Cached:     cached,
		CacheWrite: write,
	})
}
