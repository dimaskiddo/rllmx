package opencode

import (
	"slices"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"rllmx/app/llm/executor"
	"rllmx/app/store/document"
)

// The Responses lane's field names, which the Chat lane does not use.
const (
	openCodeMaxToolName = 128
	openCodeMaxCallID   = 64
)

// openCodeResponsesBody fits a Chat-Completions body to the Responses lane the
// gateway serves some models on: an input list rather than messages, the output
// cap under its own name, the effort as a reasoning object, no reasoning items
// carried over, and store off.
func openCodeResponsesBody(body []byte, capsLevels []string) []byte {
	body = normalizeOpenCodeInput(body)
	body = openCodeOutputCap(body)
	body = openCodeReasoning(body, capsLevels)
	body = executor.SetValue(body, "stream", true)
	body = executor.SetValue(body, "store", false)
	body = openCodeFlatTools(body)
	return openCodeDropReasoningItems(body)
}

// normalizeOpenCodeInput turns a string input into one user message and gives an
// empty one a placeholder, which the upstream refuses to see missing.
func normalizeOpenCodeInput(body []byte) []byte {
	in := gjson.GetBytes(body, "input")
	switch {
	case !in.Exists():
		return body
	case in.Type == gjson.String:
		text := in.String()
		if strings.TrimSpace(text) == "" {
			text = "..."
		}
		return executor.SetRaw(body, "input", `[{"type":"message","role":"user","content":[{"type":"input_text","text":`+document.Encode(text)+`}]}]`)
	case in.IsArray() && len(in.Array()) == 0:
		return executor.SetRaw(body, "input", `[{"type":"message","role":"user","content":[{"type":"input_text","text":"..."}]}]`)
	}
	return body
}

// openCodeOutputCap moves the output cap to the field the Responses lane reads.
func openCodeOutputCap(body []byte) []byte {
	if gjson.GetBytes(body, "max_output_tokens").Exists() {
		return executor.Remove(executor.Remove(body, "max_tokens"), "max_completion_tokens")
	}
	for _, from := range []string{"max_completion_tokens", "max_tokens"} {
		if v := gjson.GetBytes(body, from); v.Exists() {
			body = executor.SetValue(body, "max_output_tokens", v.Value())
			break
		}
	}
	return executor.Remove(executor.Remove(body, "max_tokens"), "max_completion_tokens")
}

// openCodeReasoning writes the effort as the object the lane reads. A level
// above what the model declares moves down to the nearest one it does.
func openCodeReasoning(body []byte, levels []string) []byte {
	effort := gjson.GetBytes(body, "reasoning_effort").String()
	if effort == "" {
		if r := gjson.GetBytes(body, "reasoning.effort").String(); r != "" {
			effort = r
			body = executor.SetValue(body, "reasoning.summary", "auto")
			return body
		}
		if gjson.GetBytes(body, "reasoning").IsObject() {
			return executor.SetValue(body, "reasoning.summary", "auto")
		}
		return body
	}
	effort = strings.ToLower(strings.TrimSpace(effort))
	if (effort == "max" || effort == "ultra") && len(levels) > 0 && !slices.Contains(levels, effort) {
		if effort == "ultra" && slices.Contains(levels, "max") {
			effort = "max"
		} else if slices.Contains(levels, "xhigh") {
			effort = "xhigh"
		}
	}
	body = executor.Remove(body, "reasoning_effort")
	body = executor.SetValue(body, "reasoning.effort", effort)
	return executor.SetValue(body, "reasoning.summary", "auto")
}

// openCodeFlatTools puts the tools in the flat shape the lane takes and drops a
// chosen tool that the request no longer carries.
func openCodeFlatTools(body []byte) []byte {
	if !gjson.GetBytes(body, "tools").IsArray() {
		return body
	}
	var out []string
	names := map[string]bool{}
	for _, t := range gjson.GetBytes(body, "tools").Array() {
		if !t.IsObject() {
			continue
		}
		fn := t.Get("function")
		name := strings.TrimSpace(document.FirstString(t.Get("name"), fn.Get("name")))
		if name == "" {
			continue
		}
		if len(name) > openCodeMaxToolName {
			name = name[:openCodeMaxToolName]
		}
		flat := `{"type":"function"}`
		flat, _ = sjson.Set(flat, "name", name)
		if d := document.FirstString(t.Get("description"), fn.Get("description")); d != "" {
			flat, _ = sjson.Set(flat, "description", d)
		}
		params := `{"type":"object","properties":{}}`
		switch {
		case t.Get("parameters").IsObject():
			params = withProperties(t.Get("parameters").Raw)
		case fn.Get("parameters").IsObject():
			params = withProperties(fn.Get("parameters").Raw)
		}
		flat, _ = sjson.SetRaw(flat, "parameters", params)
		names[name] = true
		out = append(out, flat)
	}
	body = executor.SetRaw(body, "tools", "["+strings.Join(out, ",")+"]")
	if c := gjson.GetBytes(body, "tool_choice"); c.Get("type").String() == "function" {
		if n := strings.TrimSpace(c.Get("name").String()); n == "" || !names[n] {
			body = executor.Remove(body, "tool_choice")
		}
	}
	if !gjson.GetBytes(body, "tool_choice").Exists() {
		body = executor.SetValue(body, "tool_choice", "auto")
	}
	return body
}

// withProperties fills an empty properties map, which a strict backend answers
// as an invalid schema.
func withProperties(schema string) string {
	if gjson.Get(schema, "type").String() == "object" && !gjson.Get(schema, "properties").Exists() {
		if out, err := sjson.SetRaw(schema, "properties", `{}`); err == nil {
			return out
		}
	}
	return schema
}

// openCodeDropReasoningItems takes out the reasoning items a previous turn left
// and the encrypted content that cannot be read by another account.
func openCodeDropReasoningItems(body []byte) []byte {
	in := gjson.GetBytes(body, "input")
	if !in.IsArray() {
		return body
	}
	out := make([]string, 0, len(in.Array()))
	for _, item := range in.Array() {
		if !item.IsObject() {
			out = append(out, item.Raw)
			continue
		}
		switch item.Get("type").String() {
		case "reasoning":
			continue
		case "function_call":
			if strings.TrimSpace(item.Get("name").String()) == "" {
				continue
			}
			call := item.Raw
			call, _ = sjson.Set(call, "name", cutName(item.Get("name").String()))
			call = openCodeCallID(call)
			out = append(out, call)
		case "function_call_output":
			out = append(out, openCodeCallID(item.Raw))
		default:
			raw := item.Raw
			raw, _ = sjson.Delete(raw, "encrypted_content")
			out = append(out, raw)
		}
	}
	return executor.SetRaw(body, "input", "["+strings.Join(out, ",")+"]")
}

// openCodeCallID holds a call id to the length a strict backend reads.
func openCodeCallID(item string) string {
	id := gjson.Get(item, "call_id").String()
	if id == "" || len(id) > openCodeMaxCallID {
		return item
	}
	return item
}

func cutName(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > openCodeMaxToolName {
		return s[:openCodeMaxToolName]
	}
	return s
}
