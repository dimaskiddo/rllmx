package translate

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"rllmx/app/upstream"
)

// ApplyParams runs the parameter rules over a final upstream body, which is why
// it takes bytes: it is the last step, after every injection, on every pair, so
// a rule is stated once and holds on every route. An unreadable body is
// returned unchanged. The body is edited in place, so key order, number text
// and unknown fields keep their bytes.
func ApplyParams(body []byte, rules []upstream.ParamRule) []byte {
	if len(rules) == 0 || !gjson.ValidBytes(body) || !IsObject(body) {
		return body
	}
	for _, rule := range rules {
		for _, f := range rule.Drop {
			body = deleteTop(body, f)
		}
		for from, to := range rule.Rename {
			v, ok := topField(body, from)
			if !ok {
				continue
			}
			body = deleteTop(body, from)
			if _, taken := topField(body, to); !taken {
				body = setTop(body, to, v.Raw)
			}
		}
		if len(rule.DropMessageFields) > 0 || rule.FlattenContent {
			body = rewriteMessages(body, rule)
		}
		if rule.CapOutput > 0 {
			for _, key := range []string{"max_tokens", "max_completion_tokens"} {
				// Only a whole number is capped: a fractional or exponent form is
				// not a valid limit and is left for the upstream to refuse.
				if v, ok := topField(body, key); ok && v.Type == gjson.Number {
					if n, err := strconv.ParseInt(v.Raw, 10, 64); err == nil && n > int64(rule.CapOutput) {
						body = setTop(body, key, strconv.Itoa(rule.CapOutput))
					}
				}
			}
		}
	}
	return body
}

// sortedRaw wraps raw fields so they encode with sorted keys.
func sortedRaw(m map[string]json.RawMessage) obj {
	o := make(obj, len(m))
	for k, v := range m {
		o[k] = v
	}
	return o
}

// topField reads a field of the top-level object by its exact name, whatever
// characters the name holds.
func topField(body []byte, name string) (gjson.Result, bool) {
	var found gjson.Result
	gjson.ParseBytes(body).ForEach(func(k, v gjson.Result) bool {
		if k.String() == name {
			found = v
			return false
		}
		return true
	})
	return found, found.Exists()
}

// deleteTop removes a top-level field by its exact name. A path would read a
// dot, a star or a hash in the name as syntax.
func deleteTop(body []byte, name string) []byte {
	if _, ok := topField(body, name); !ok {
		return body
	}
	if next, err := sjson.DeleteBytes(body, escapePath(name)); err == nil {
		return next
	}
	return body
}

func setTop(body []byte, name, raw string) []byte {
	if next, err := sjson.SetRawBytes(body, escapePath(name), []byte(raw)); err == nil {
		return next
	}
	return body
}

// escapePath makes a field name one literal path segment.
func escapePath(name string) string {
	if !strings.ContainsAny(name, `.*?#|@\:!=<>%"`) {
		return name
	}
	var b strings.Builder
	for i := 0; i < len(name); i++ {
		if strings.IndexByte(`.*?#|@\:!=<>%"`, name[i]) >= 0 {
			b.WriteByte('\\')
		}
		b.WriteByte(name[i])
	}
	return b.String()
}

// rewriteMessages edits the assistant messages and flattens text-only content,
// one message at a time, and writes the array back once.
func rewriteMessages(body []byte, rule upstream.ParamRule) []byte {
	msgs := gjson.GetBytes(body, "messages")
	if !msgs.IsArray() {
		return body
	}
	list := msgs.Array()
	for _, m := range list {
		// A body whose messages are not all objects is not one a rule can
		// reason about, so none of it is rewritten. A null stays null; the map
		// version turned it into an empty object.
		if !m.IsObject() && m.Type != gjson.Null {
			return body
		}
	}
	var out strings.Builder
	last, changed := 0, false
	for _, m := range list {
		if !m.IsObject() {
			continue
		}
		raw := m.Raw
		if m.Get("role").String() == "assistant" {
			for _, f := range rule.DropMessageFields {
				if _, ok := topField([]byte(raw), f); ok {
					if next, err := sjson.Delete(raw, escapePath(f)); err == nil {
						raw = next
					}
				}
			}
		}
		if rule.FlattenContent {
			if text, ok := flatText(gjson.Get(raw, "content")); ok {
				if next, err := sjson.Set(raw, "content", text); err == nil {
					raw = next
				}
			}
		}
		if raw == m.Raw {
			continue
		}
		if !changed {
			out.Grow(len(msgs.Raw) + 64)
			changed = true
		}
		start := m.Index - msgs.Index
		out.WriteString(msgs.Raw[last:start])
		out.WriteString(raw)
		last = start + len(m.Raw)
	}
	if !changed {
		return body
	}
	out.WriteString(msgs.Raw[last:])
	next, err := sjson.SetRawBytes(body, "messages", []byte(out.String()))
	if err != nil {
		return body
	}
	return next
}

// flatText joins a content array made only of text parts. Any other part, or an
// empty array, leaves the content as it is.
func flatText(content gjson.Result) (string, bool) {
	if !content.IsArray() {
		return "", false
	}
	var b strings.Builder
	n := 0
	for _, p := range content.Array() {
		if p.Get("type").String() != "text" {
			return "", false
		}
		b.WriteString(p.Get("text").String())
		n++
	}
	return b.String(), n > 0
}
