package translate

import (
	"encoding/json"
	"strconv"
	"strings"
)

// kiroTurns turns hub messages into the turns a Kiro conversation is made of.
// A system message joins the first user turn, a tool result becomes the user
// turn that answers the call before it, and an image rides as base64.
func kiroTurns(r *Request) []kiroTurn {
	var turns []kiroTurn
	for _, m := range r.Messages {
		switch m.Role {
		case RoleSystem:
			continue
		case RoleAssistant:
			a := &kiroAsst{Content: m.Text()}
			for _, call := range m.ToolCalls {
				a.Calls = append(a.Calls, kiroCall{
					ToolUseID: call.ID, Name: call.Name, Input: kiroArgs(call.Arguments),
				})
			}
			turns = append(turns, kiroTurn{Asst: a})
		default:
			u := kiroUser{}
			for _, p := range m.Content {
				switch p.Type {
				case PartText:
					appendContent(&u.Content, p.Text)
				case PartImage:
					if img, ok := kiroImageOf(p); ok {
						u.Images = append(u.Images, img)
						continue
					}
					// The upstream takes base64 only, so a remote image keeps its
					// address as text rather than vanishing.
					if p.URL != "" {
						appendContent(&u.Content, "[Image: "+p.URL+"]")
					}
				}
			}
			if m.Role == RoleTool {
				u.Results = append(u.Results, kiroResult{
					ToolUseID: m.ToolCallID, Status: "success",
					Content: []kiroResultText{{Text: m.Text()}},
				})
			}
			turns = append(turns, kiroTurn{User: &u})
		}
	}
	return turns
}

// kiroArgs keeps a call's arguments as JSON. Text that is not an object is not
// something the upstream can carry, so it goes as an empty one.
func kiroArgs(args string) json.RawMessage {
	trimmed := strings.TrimSpace(args)
	if trimmed == "" {
		return json.RawMessage(`{}`)
	}
	var v any
	if json.Unmarshal([]byte(trimmed), &v) != nil {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(trimmed)
}

// kiroImageOf reads an image part the upstream accepts: base64 with a media
// type, which is the only form it carries.
func kiroImageOf(p Part) (kiroImage, bool) {
	data, media := p.Data, p.MediaType
	if data == "" {
		if m, d, ok := splitDataURI(p.URL); ok {
			data, media = d, m
		}
	}
	if data == "" || media == "" {
		return kiroImage{}, false
	}
	var img kiroImage
	img.Format = kiroFormat(media)
	img.Source.Bytes = data
	return img, true
}

// kiroFormat is the media type's subtype, which is what the field names.
func kiroFormat(media string) string {
	if i := strings.IndexByte(media, '/'); i >= 0 {
		return media[i+1:]
	}
	return media
}

// kiroPrefix is what goes in front of the first user turn: the thinking
// trigger, the agentic prompt and the client's own system text, then the time.
func kiroPrefix(r *Request, c *Ctx, thinking, agentic bool) string {
	var parts []string
	if thinking {
		parts = append(parts, kiroThinkingTag(kiroBudget(r, c)))
	}
	if agentic {
		parts = append(parts, kiroAgenticPrompt)
	}
	parts = append(parts, systemText(r))
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

// kiroThinkingTag asks the model to think with the budget a client or the model
// default set, held to what the upstream accepts.
func kiroThinkingTag(budget int) string {
	if budget <= 0 {
		budget = kiroThinkingDefault
	}
	budget = min(budget, kiroThinkingMax)
	return "<thinking_mode>enabled</thinking_mode>\n<max_thinking_length>" + strconv.Itoa(budget) + "</max_thinking_length>"
}

// kiroBudget is the token budget the request asks for, or the default.
func kiroBudget(r *Request, c *Ctx) int {
	if r.Thinking.Mode == "budget" && r.Thinking.Budget > 0 {
		return r.Thinking.Budget
	}
	if r.Thinking.Level != "" {
		if b := c.Budget(r.Thinking.Level); b > 0 {
			return b
		}
	}
	return kiroThinkingDefault
}

// systemText joins the hub's system text.
func systemText(r *Request) string {
	var parts []string
	for _, m := range r.Messages {
		if m.Role == RoleSystem {
			if t := strings.TrimSpace(m.Text()); t != "" {
				parts = append(parts, t)
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

// prefixFirstUser puts the prefix on the first user turn, creating one when the
// conversation opens with the model's own words.
func prefixFirstUser(turns []kiroTurn, prefix string) []kiroTurn {
	if prefix == "" {
		return turns
	}
	for i := range turns {
		if turns[i].User == nil {
			continue
		}
		appendFront(&turns[i].User.Content, prefix)
		return turns
	}
	return append([]kiroTurn{{User: &kiroUser{Content: prefix}}}, turns...)
}

// appendFront puts text ahead of what a turn already says.
func appendFront(dst *string, text string) {
	if *dst == "" {
		*dst = text
		return
	}
	*dst = text + "\n\n" + *dst
}

// kiroEffort is the native thinking shape for a model that takes one, and nil
// for a model that does not: those are asked with the prompt tag instead.
func kiroEffort(r *Request, model string) map[string]any {
	level := r.Thinking.Level
	if level == "" || r.Thinking.Mode == "budget" {
		return nil
	}
	if strings.Contains(model, "gpt") {
		switch level {
		case "low", "medium", "high", "xhigh":
			return map[string]any{"reasoning": map[string]any{"effort": level}}
		case "max":
			return map[string]any{"reasoning": map[string]any{"effort": "xhigh"}}
		}
		return nil
	}
	if !strings.Contains(model, "claude") {
		return nil
	}
	if level == "xhigh" && kiroIsLegacy(model) {
		level = "high"
	}
	return map[string]any{
		"thinking":      map[string]any{"type": "adaptive", "display": "summarized"},
		"output_config": map[string]any{"effort": level},
	}
}

// kiroIsLegacy reports a model that predates the effort fields.
func kiroIsLegacy(model string) bool {
	switch model {
	case "claude-opus-4.5", "claude-sonnet-4.5", "claude-sonnet-4", "claude-haiku-4.5":
		return true
	}
	return false
}

// kiroInference is the sampling block, when the request set one.
func kiroInference(r *Request) map[string]any {
	out := map[string]any{}
	if r.Params.MaxTokens != nil && *r.Params.MaxTokens > 0 {
		out["maxTokens"] = *r.Params.MaxTokens
	}
	if r.Params.Temperature != nil {
		out["temperature"] = *r.Params.Temperature
	}
	if r.Params.TopP != nil {
		out["topP"] = *r.Params.TopP
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// kiroWireUser is one user turn as the body names it.
func kiroWireUser(u kiroUser) map[string]any {
	out := map[string]any{"content": u.Content, "modelId": u.ModelID, "origin": kiroOrigin}
	if len(u.Images) > 0 {
		out["images"] = u.Images
	}
	ctx := map[string]any{}
	if len(u.Results) > 0 {
		ctx["toolResults"] = u.Results
	}
	if len(u.Tools) > 0 {
		ctx["tools"] = u.Tools
	}
	if len(ctx) > 0 {
		out["userInputMessageContext"] = ctx
	}
	return out
}

// kiroOrigin is the marker the client the account belongs to sends.
const kiroOrigin = "AI_EDITOR"

// kiroWireHistory is the turns before the current one.
func kiroWireHistory(turns []kiroTurn, model string) []map[string]any {
	out := make([]map[string]any, 0, len(turns))
	for _, t := range turns {
		if t.User != nil {
			u := *t.User
			u.ModelID = model
			out = append(out, map[string]any{"userInputMessage": kiroWireUser(u)})
			continue
		}
		a := map[string]any{"content": t.Asst.Content}
		if len(t.Asst.Calls) > 0 {
			a["toolUses"] = t.Asst.Calls
		}
		out = append(out, map[string]any{"assistantResponseMessage": a})
	}
	return out
}

// replayKiro puts the conversation's opening turn in front. A previous request
// froze that turn's own words, so a later one replays them exactly and adds
// only the volatile part to the newest turn — which is what keeps the
// upstream's prompt cache hitting.
func replayKiro(turns []kiroTurn, c *Ctx, prefix string) []kiroTurn {
	opening, rest := splitFirstUser(turns)
	if opening == "" {
		return prefixFirstUser(turns, prefix)
	}
	marked := opening
	if c.Replay != nil {
		if frozen, ok := c.Replay.Frozen(c.Session); ok {
			marked = frozen
		} else {
			c.Replay.Freeze(c.Session, opening)
		}
	}
	rest = prefixFirstUser(rest, prefix)
	return append([]kiroTurn{{User: &kiroUser{Content: marked}}}, rest...)
}

// splitFirstUser takes the first user turn's own text off the front of a
// conversation, leaving the turns that follow it.
func splitFirstUser(turns []kiroTurn) (string, []kiroTurn) {
	for i := range turns {
		if turns[i].User == nil {
			continue
		}
		text := turns[i].User.Content
		rest := append([]kiroTurn{}, turns[:i]...)
		rest = append(rest, turns[i+1:]...)
		return text, rest
	}
	return "", turns
}
