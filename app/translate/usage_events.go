package translate

import "github.com/tidwall/gjson"

// ReadUsageEvent finds the usage report in one stream event of a format. It is
// the only place a stream's usage is read, for the translating readers and for
// the transport that records the row.
func ReadUsageEvent(format string, data []byte) (Usage, bool) {
	// GetBytes looks the path up in place and copies only the value it finds,
	// where ParseBytes would copy the whole event first.
	get := func(path string) gjson.Result { return gjson.GetBytes(data, path) }
	if !IsObject(data) {
		return Usage{}, false
	}
	switch format {
	case FormatOpenAI:
		if u := get("usage"); u.IsObject() && u.Get("prompt_tokens").Exists() {
			return readOpenAIUsage(u), true
		}
	case FormatClaude:
		switch get("type").String() {
		case "message_start":
			if u := get("message.usage"); u.IsObject() {
				return readClaudeUsage(u), true
			}
		case "message_delta":
			if u := get("usage"); u.IsObject() {
				return readClaudeUsage(u), true
			}
		}
	case FormatResponses:
		if u := get("response.usage"); u.IsObject() {
			return readResponsesUsage(u), true
		}
		if u := get("usage"); u.IsObject() && get("type").String() != "" {
			return readResponsesUsage(u), true
		}
	case FormatGemini:
		for _, p := range []string{"usageMetadata", "response.usageMetadata"} {
			if u := get(p); u.IsObject() {
				return readGeminiUsage(u), true
			}
		}
	case FormatOllama:
		if get("done").Bool() && get("prompt_eval_count").Exists() {
			return NormalizeUsage(Usage{
				Prompt: get("prompt_eval_count").Int(), Completion: get("eval_count").Int(),
				Cached: get("prompt_eval_cached_count").Int(),
			}), true
		}
	}
	return Usage{}, false
}

// IsObject reports whether the first non-space byte opens an object, which is
// what gjson.Result.IsObject says of the whole value.
func IsObject(data []byte) bool {
	for _, c := range data {
		switch c {
		case ' ', '\t', '\n', '\r':
			continue
		}
		return c == '{'
	}
	return false
}
