package openai

import (
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"rllmx/app/llm/executor"
	"rllmx/app/platform/text"
	"rllmx/app/store/document"
)

const maxToolName = 128

// hostedTools are the tool types a Responses backend runs itself.
var hostedTools = map[string]bool{
	"image_generation": true, "web_search": true, "web_search_preview": true, "file_search": true,
	"computer": true, "computer_use_preview": true, "code_interpreter": true, "mcp": true,
	"local_shell": true, "tool_search": true, "x_search": true,
}

// keepOnly rebuilds the body from the allowed top-level fields only. A backend
// that sees an unknown field answers "unsupported" for the whole request. The
// object is rebuilt rather than edited by path, so no field name, however odd,
// can be left behind.
func keepOnly(body []byte, allowed map[string]bool) []byte {
	out := make([]byte, 1, len(body)+2)
	out[0] = '{'
	gjson.ParseBytes(body).ForEach(func(k, v gjson.Result) bool {
		if !allowed[k.String()] {
			return true
		}
		if len(out) > 1 {
			out = append(out, ',')
		}
		out = append(out, k.Raw...)
		out = append(out, ':')
		out = append(out, v.Raw...)
		return true
	})
	return append(out, '}')
}

// flattenTools turns Chat Completions tools into the flat Responses shape,
// keeps hosted tools as they are and drops anything else. It returns the new
// list and the names of the function tools, which a forced choice must match.
// A forced choice that names no tool is removed by the caller.
func flattenTools(body []byte, schema func(string) string, freeform string) ([]byte, map[string]bool) {
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return body, nil
	}
	names := map[string]bool{}
	out := make([]string, 0, len(tools.Array()))
	for _, t := range tools.Array() {
		if !t.IsObject() {
			continue
		}
		typ := t.Get("type").String()
		if typ == "namespace" {
			for _, sub := range t.Get("tools").Array() {
				if n := strings.TrimSpace(sub.Get("name").String()); n != "" {
					names[cut(n)] = true
				}
			}
			out = append(out, t.Raw)
			continue
		}
		if typ == "custom" && freeform == "" {
			out = append(out, t.Raw)
			continue
		}
		if hostedTools[typ] {
			out = append(out, t.Raw)
			continue
		}
		fn := t.Get("function")
		if typ != "function" && typ != "custom" && typ != "" {
			continue
		}
		if typ == "" && !fn.IsObject() && !t.Get("name").Exists() {
			continue
		}
		name := strings.TrimSpace(document.FirstString(t.Get("name"), fn.Get("name")))
		if name == "" {
			continue
		}
		flat := `{"type":"function"}`
		flat, _ = sjson.Set(flat, "name", cut(name))
		if d := document.FirstString(t.Get("description"), fn.Get("description")); d != "" {
			flat, _ = sjson.Set(flat, "description", d)
		}
		params := `{"type":"object","properties":{}}`
		switch {
		case typ == "custom" && freeform != "":
			params = freeform
		case t.Get("parameters").IsObject():
			params = schema(t.Get("parameters").Raw)
		case fn.Get("parameters").IsObject():
			params = schema(fn.Get("parameters").Raw)
		}
		flat, _ = sjson.SetRaw(flat, "parameters", params)
		if s := t.Get("strict"); s.Type == gjson.True || s.Type == gjson.False {
			flat, _ = sjson.Set(flat, "strict", s.Bool())
		} else if s := fn.Get("strict"); s.Type == gjson.True || s.Type == gjson.False {
			flat, _ = sjson.Set(flat, "strict", s.Bool())
		}
		names[cut(name)] = true
		out = append(out, flat)
	}
	return executor.SetRaw(body, "tools", "["+strings.Join(out, ",")+"]"), names
}

func cut(s string) string { return text.CutBytes(s, maxToolName) }

// fixToolChoice drops a forced choice that names a function the request does
// not carry.
func fixToolChoice(body []byte, names map[string]bool) []byte {
	tc := gjson.GetBytes(body, "tool_choice")
	if !tc.IsObject() || tc.Get("type").String() != "function" {
		return body
	}
	n := strings.TrimSpace(tc.Get("name").String())
	if n == "" || !names[cut(n)] {
		return executor.Remove(body, "tool_choice")
	}
	return body
}

// unicodeProperty finds \p{ or \P{ preceded by an odd number of backslashes: an
// even number means the backslash is itself escaped and the "p" is literal.
var unicodeProperty = regexp.MustCompile(`(^|[^\\])(\\\\)*\\[pP]\{`)

// stripUnicodePatterns removes every "pattern" constraint that holds a Unicode
// property escape, at any depth. The Codex backend validates schemas with an
// engine that has none, and answers 400 for the whole request. Keys under
// "properties" are property names, never keywords. A schema with nothing to
// remove comes back byte for byte.
func stripUnicodePatterns(schema string) string {
	if !strings.Contains(schema, `\\`) && !strings.Contains(schema, "pattern") {
		return schema
	}
	out, changed := stripNode(gjson.Parse(schema))
	if !changed {
		return schema
	}
	return out
}

func stripNode(n gjson.Result) (string, bool) {
	switch {
	case n.IsArray():
		parts := make([]string, 0, len(n.Array()))
		changed := false
		for _, item := range n.Array() {
			s, c := stripNode(item)
			changed = changed || c
			parts = append(parts, s)
		}
		if !changed {
			return n.Raw, false
		}
		return "[" + strings.Join(parts, ",") + "]", true
	case n.IsObject():
		return stripObject(n, false)
	}
	return n.Raw, false
}

// stripObject rebuilds an object. names is true for the value of "properties",
// whose keys are names and whose values are schemas.
func stripObject(n gjson.Result, names bool) (string, bool) {
	var b strings.Builder
	b.WriteByte('{')
	first, changed := true, false
	n.ForEach(func(k, v gjson.Result) bool {
		key := k.String()
		if !names && key == "pattern" && v.Type == gjson.String && unicodeProperty.MatchString(v.String()) {
			changed = true
			return true
		}
		var val string
		var c bool
		switch {
		case !names && key == "properties" && v.IsObject():
			val, c = stripObject(v, true)
		case names:
			val, c = stripNode(v)
		default:
			val, c = stripNode(v)
		}
		changed = changed || c
		if !first {
			b.WriteByte(',')
		}
		first = false
		b.WriteString(k.Raw)
		b.WriteByte(':')
		b.WriteString(val)
		return true
	})
	b.WriteByte('}')
	if !changed {
		return n.Raw, false
	}
	return b.String(), true
}
