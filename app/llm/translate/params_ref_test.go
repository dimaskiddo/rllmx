package translate

import (
	"encoding/json"

	"rllmx/app/llm/upstream"
)

// applyParamsReference is the map round trip ApplyParams replaced. It sorts
// every key, which an in-place edit does not, so the comparison tests below
// feed it bodies whose keys are already sorted, as every codec writes them.
func applyParamsReference(body []byte, rules []upstream.ParamRule) []byte {
	if len(rules) == 0 {
		return body
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(body, &top) != nil {
		return body
	}
	for _, rule := range rules {
		for _, f := range rule.Drop {
			delete(top, f)
		}
		for from, to := range rule.Rename {
			if v, ok := top[from]; ok {
				delete(top, from)
				if _, taken := top[to]; !taken {
					top[to] = v
				}
			}
		}
		if len(rule.DropMessageFields) > 0 || rule.FlattenContent {
			top = rewriteMessagesReference(top, rule)
		}
		if rule.CapOutput > 0 {
			for _, key := range []string{"max_tokens", "max_completion_tokens"} {
				var n int
				if v, ok := top[key]; ok && json.Unmarshal(v, &n) == nil && n > rule.CapOutput {
					top[key], _ = json.Marshal(rule.CapOutput)
				}
			}
		}
	}
	out, err := json.Marshal(SortedRaw(top))
	if err != nil {
		return body
	}
	return out
}

func rewriteMessagesReference(top map[string]json.RawMessage, rule upstream.ParamRule) map[string]json.RawMessage {
	var msgs []map[string]json.RawMessage
	if json.Unmarshal(top["messages"], &msgs) != nil {
		return top
	}
	for _, m := range msgs {
		var role string
		_ = json.Unmarshal(m["role"], &role)
		if role == "assistant" {
			for _, f := range rule.DropMessageFields {
				delete(m, f)
			}
		}
		if rule.FlattenContent {
			var parts []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if json.Unmarshal(m["content"], &parts) == nil && len(parts) > 0 {
				text, only := "", true
				for _, p := range parts {
					if p.Type != "text" {
						only = false
						break
					}
					text += p.Text
				}
				if only {
					m["content"], _ = json.Marshal(text)
				}
			}
		}
	}
	list := make([]Obj, len(msgs))
	for i, m := range msgs {
		list[i] = SortedRaw(m)
	}
	top["messages"], _ = json.Marshal(list)
	return top
}
