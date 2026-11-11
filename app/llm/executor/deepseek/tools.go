package deepseek

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/executor"
	"rllmx/app/store/dialect"
)

// toolCall is one call parsed out of the model's text.
type toolCall struct {
	ID        string
	Name      string
	Arguments string
}

// toolManifest is the prompt block that offers the tools and names the binding
// every call must carry. Its wording follows the reference's: it forces the one
// canonical shape and forbids the alternatives the model tends to invent.
func toolManifest(tools gjson.Result, nonce string) string {
	var lines []string
	for _, t := range tools.Array() {
		name := t.Get("function.name").String()
		if name == "" {
			continue
		}
		line := "- " + name
		if d := t.Get("function.description").String(); d != "" {
			line += ": " + d
		}
		if params := t.Get("function.parameters"); params.Exists() {
			line += "\n  parameters: " + params.Raw
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 || nonce == "" {
		return ""
	}
	return strings.Join([]string{
		"You can call tools. To call a tool, output ONLY this exact block (no markdown fence):",
		`<tool>{"name": "<tool_name>", "arguments": { ... }, "_nonce": "` + nonce + `"}</tool>`,
		"Rules:",
		"- Use exactly <tool>...</tool>. Do NOT use other tags, attributes, markers, or code fences.",
		`- Include the secret binding "_nonce": "` + nonce + `" exactly as shown.`,
		`- "name" must be one of the tools below; "arguments" must be a JSON object.`,
		"- When a tool is needed, emit the <tool> block instead of only describing the plan.",
		"- Emit one <tool> block per call; you may put several blocks back to back.",
		"- If no tool is needed, just answer normally without any <tool> block.",
		"",
		"Available tools:",
		strings.Join(lines, "\n"),
	}, "\n")
}

// toolConversation replays the whole trajectory, including the calls the model
// already made and their results, so an agent loop advances instead of
// restarting from the original task.
func toolConversation(messages []gjson.Result, manifest string) string {
	var system, lines []string
	if manifest != "" {
		system = append(system, manifest)
	}
	names := map[string]string{}
	active := false
	for _, m := range messages {
		text := strings.TrimSpace(messageText(m.Get("content")))
		switch m.Get("role").String() {
		case "system":
			if text != "" {
				system = append(system, text)
			}
		case "user":
			if text != "" {
				lines = append(lines, "User: "+text)
			}
		case "assistant":
			var parts []string
			if text != "" {
				parts = append(parts, text)
			}
			for _, c := range m.Get("tool_calls").Array() {
				name := c.Get("function.name").String()
				args := c.Get("function.arguments").String()
				if args == "" {
					args = "{}"
				}
				if id := c.Get("id").String(); id != "" {
					names[id] = name
				}
				parts = append(parts, `<tool>{"name": `+quote(name)+`, "arguments": `+args+`}</tool>`)
				active = true
			}
			if len(parts) > 0 {
				lines = append(lines, "Assistant: "+strings.Join(parts, "\n"))
			}
		case "tool":
			name := m.Get("name").String()
			if id := m.Get("tool_call_id").String(); names[id] != "" {
				name = names[id]
			}
			if name == "" {
				name = "tool"
			}
			lines = append(lines, "Tool result ("+name+"): "+orDefault(text, "(no output)"))
			active = true
		}
	}
	var parts []string
	if len(system) > 0 {
		parts = append(parts, strings.Join(system, "\n\n"))
	}
	if len(lines) > 0 {
		parts = append(parts, strings.Join(lines, "\n\n"))
	}
	if active {
		parts = append(parts, "Continue the task using the tool results above. Do NOT repeat tool calls that already succeeded; perform the next step or give the final answer.")
	}
	return imageLink.ReplaceAllString(strings.Join(parts, "\n\n"), "")
}

// parseCalls reads the canonical tool blocks out of the model's text and
// returns the calls with those blocks removed. A block whose binding, name or
// arguments do not hold is left in the text: it is not a call this router will
// make, and showing it is better than dropping the answer.
func parseCalls(text, nonce string, offered map[string]bool) ([]toolCall, string) {
	var calls []toolCall
	var kept strings.Builder
	rest := text
	for {
		start := strings.Index(rest, "<tool>")
		if start < 0 {
			kept.WriteString(rest)
			break
		}
		end := strings.Index(rest[start:], "</tool>")
		if end < 0 {
			kept.WriteString(rest)
			break
		}
		kept.WriteString(rest[:start])
		block := rest[start+len("<tool>") : start+end]
		if call, ok := toolBlock(block, nonce, offered); ok {
			calls = append(calls, call)
		} else {
			kept.WriteString(rest[start : start+end+len("</tool>")])
		}
		rest = rest[start+end+len("</tool>"):]
	}
	return calls, strings.TrimSpace(kept.String())
}

// toolBlock validates one block's JSON body.
func toolBlock(block, nonce string, offered map[string]bool) (toolCall, bool) {
	var body struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
		Nonce     string          `json:"_nonce"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(block)), &body) != nil {
		return toolCall{}, false
	}
	if body.Nonce != nonce || !offered[body.Name] || len(body.Arguments) == 0 {
		return toolCall{}, false
	}
	if !gjson.ParseBytes(body.Arguments).IsObject() {
		return toolCall{}, false
	}
	return toolCall{Name: body.Name, Arguments: string(body.Arguments)}, true
}

// offeredNames is the set of tool names a request may call.
func offeredNames(tools gjson.Result) map[string]bool {
	out := map[string]bool{}
	for _, t := range tools.Array() {
		if name := t.Get("function.name").String(); name != "" {
			out[name] = true
		}
	}
	return out
}

// quote encodes a string as JSON text.
func quote(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw)
}

// orDefault returns s, or fallback when s is empty.
func orDefault(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// buildPlan is the request's prompt and tool contract, computed once.
func buildPlan(req executor.Request, body gjson.Result) plan {
	window := intField(func(name string) string { return executor.Field(req, name) }, "historyWindow")
	tools := body.Get("tools")
	if !tools.IsArray() || len(tools.Array()) == 0 {
		return plan{prompt: prompt(body.Get("messages").Array(), window)}
	}
	p := plan{hasTools: true, nonce: toolNonce(), names: offeredNames(tools)}
	p.prompt = toolConversation(body.Get("messages").Array(), toolManifest(tools, p.nonce))
	return p
}

// toolNonce is the per-request binding a call must echo. Eight hex characters
// are enough: it tells a call this router asked for from one copied into the
// conversation.
func toolNonce() string {
	return dialect.NewID()[:8]
}

// synthesize writes the buffered answer as an SSE body: a call set when the
// model asked for tools, otherwise the text.
func synthesize(model, text string, calls []toolCall, now int64, id string) string {
	var b strings.Builder
	emit := func(d delta, finish string) {
		var reason *string
		if finish != "" {
			reason = &finish
		}
		raw, err := json.Marshal(chunk{ID: id, Object: "chat.completion.chunk", Created: now, Model: model,
			Choices: []choice{{Delta: d, FinishReason: reason}}})
		if err != nil {
			return
		}
		b.WriteString("data: ")
		b.Write(raw)
		b.WriteString("\n\n")
	}
	if len(calls) > 0 {
		var deltas []toolDelta
		for i, c := range calls {
			d := toolDelta{Index: i, ID: "call_" + c.Name + "_" + strconv.Itoa(i), Type: "function"}
			d.Function.Name = c.Name
			d.Function.Arguments = c.Arguments
			deltas = append(deltas, d)
		}
		emit(delta{Role: "assistant", ToolCalls: deltas}, "tool_calls")
		b.WriteString("data: [DONE]\n\n")
		return b.String()
	}
	if text != "" {
		emit(delta{Role: "assistant", Content: text}, "")
	}
	emit(delta{}, "stop")
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}
