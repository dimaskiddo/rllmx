package kiro

import (
	"encoding/json"
	"strconv"
	"strings"

	"rllmx/app/llm/translate"
	"rllmx/app/platform/text"
)

// Bounds Kiro puts on what it accepts, from its own schema.
const (
	kiroToolNameMax        = 64
	kiroToolIDMax          = 64
	kiroToolDescriptionMax = 10237
	// kiroToolResultsPlaceholder fills a user turn that carries only results.
	// It must not read like an instruction: "continue" made the model answer the
	// word and forget the task it was in.
	kiroToolResultsPlaceholder = "Tool results provided."
	kiroEmptyUserPlaceholder   = "continue"
)

// kiroTurn is one turn of a Kiro conversation: a user's message or the model's,
// never both.
type kiroTurn struct {
	User *kiroUser
	Asst *kiroAsst
}

type kiroUser struct {
	Content string
	ModelID string
	Images  []kiroImage
	Results []kiroResult
	// Tools ride on the last turn only, which is the one the model answers.
	Tools []kiroToolSpec
}

type kiroAsst struct {
	Content string
	Calls   []kiroCall
}

type kiroImage struct {
	Format string `json:"format"`
	Source struct {
		Bytes string `json:"bytes"`
	} `json:"source"`
}

type kiroResult struct {
	ToolUseID string           `json:"toolUseId"`
	Status    string           `json:"status"`
	Content   []kiroResultText `json:"content"`
}

type kiroResultText struct {
	Text string `json:"text"`
}

type kiroCall struct {
	ToolUseID string          `json:"toolUseId"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
}

// kiroToolSpec is one tool as the wire names it.
type kiroToolSpec struct {
	ToolSpecification struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		InputSchema struct {
			JSON json.RawMessage `json:"json"`
		} `json:"inputSchema"`
	} `json:"toolSpecification"`
}

// kiroSpecs normalizes the request's tools and reports the name each client
// tool went out under, so a call comes back with the name the client knows.
func kiroSpecs(tools []translate.Tool) ([]kiroToolSpec, map[string]string) {
	specs := make([]kiroToolSpec, 0, len(tools))
	names := make(map[string]string, len(tools))
	used := make(map[string]bool, len(tools))
	for i, t := range tools {
		raw := strings.TrimSpace(t.Name)
		if raw == "" {
			continue
		}
		// A repeated definition describes the same tool, so it is not sent twice.
		if _, dup := names[raw]; dup {
			continue
		}
		name := kiroToolName(raw, i+1, used)
		names[raw] = name
		var spec kiroToolSpec
		spec.ToolSpecification.Name = name
		spec.ToolSpecification.Description = text.CutRunes(orDefault(t.Description, "Tool: "+raw), kiroToolDescriptionMax)
		spec.ToolSpecification.InputSchema.JSON = kiroRootSchema(t.Parameters)
		specs = append(specs, spec)
	}
	return specs, names
}

// kiroToolName keeps the characters Kiro accepts, trims the ones it does not,
// shortens the result and makes it unique.
func kiroToolName(raw string, index int, used map[string]bool) string {
	var b strings.Builder
	b.Grow(len(raw))
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	name := text.CutRunes(strings.Trim(b.String(), "_"), kiroToolNameMax)
	if name == "" {
		name = "tool_" + strconv.Itoa(index)
	}
	base := name
	for n := 2; used[base]; n++ {
		suffix := "_" + strconv.Itoa(n)
		base = text.CutRunes(name, kiroToolNameMax-len(suffix)) + suffix
	}
	used[base] = true
	return base
}

// kiroRootSchema makes a JSON Schema one Kiro accepts: an object with
// properties, no empty required list and no additionalProperties.
func kiroRootSchema(raw json.RawMessage) json.RawMessage {
	clean := map[string]any{}
	if len(raw) > 0 {
		var v any
		if json.Unmarshal(raw, &v) == nil {
			if m, ok := v.(map[string]any); ok {
				clean = cleanSchema(m).(map[string]any)
			}
		}
	}
	clean["type"] = "object"
	if _, ok := clean["properties"].(map[string]any); !ok {
		clean["properties"] = map[string]any{}
	}
	if req, ok := clean["required"].([]any); ok {
		props, _ := clean["properties"].(map[string]any)
		kept := make([]any, 0, len(req))
		seen := map[string]bool{}
		for _, name := range req {
			s, ok := name.(string)
			if !ok || seen[s] {
				continue
			}
			if _, has := props[s]; !has {
				continue
			}
			seen[s] = true
			kept = append(kept, s)
		}
		if len(kept) == 0 {
			delete(clean, "required")
		} else {
			clean["required"] = kept
		}
	}
	out, err := json.Marshal(clean)
	if err != nil {
		return json.RawMessage(`{"properties":{},"type":"object"}`)
	}
	return out
}

// cleanSchema drops what Kiro refuses anywhere in a schema.
func cleanSchema(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, child := range x {
			if k == "additionalProperties" {
				continue
			}
			if k == "required" {
				if list, ok := child.([]any); ok && len(list) == 0 {
					continue
				}
			}
			out[k] = cleanSchema(child)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, child := range x {
			out[i] = cleanSchema(child)
		}
		return out
	}
	return v
}

// orDefault is s, or def when s is empty.
func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}
