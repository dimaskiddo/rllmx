package translate

import "github.com/tidwall/gjson"

// ReadUsageEvent finds the usage report in one stream event of a format. It is
// the only place a stream's usage is read, for the translating readers and for
// the transport that records the row.
func ReadUsageEvent(format string, data []byte) (Usage, bool) {
	g := gjson.ParseBytes(data)
	if !g.IsObject() {
		return Usage{}, false
	}
	switch format {
	case FormatOpenAI:
		if u := g.Get("usage"); u.IsObject() && u.Get("prompt_tokens").Exists() {
			return readOpenAIUsage(u), true
		}
	case FormatClaude:
		switch g.Get("type").String() {
		case "message_start":
			if u := g.Get("message.usage"); u.IsObject() {
				return readClaudeUsage(u), true
			}
		case "message_delta":
			if u := g.Get("usage"); u.IsObject() {
				return readClaudeUsage(u), true
			}
		}
	case FormatResponses:
		if u := g.Get("response.usage"); u.IsObject() {
			return readResponsesUsage(u), true
		}
		if u := g.Get("usage"); u.IsObject() && g.Get("type").String() != "" {
			return readResponsesUsage(u), true
		}
	case FormatGemini:
		for _, p := range []string{"usageMetadata", "response.usageMetadata"} {
			if u := g.Get(p); u.IsObject() {
				return readGeminiUsage(u), true
			}
		}
	case FormatOllama:
		if g.Get("done").Bool() && g.Get("prompt_eval_count").Exists() {
			return NormalizeUsage(Usage{
				Prompt: g.Get("prompt_eval_count").Int(), Completion: g.Get("eval_count").Int(),
				Cached: g.Get("prompt_eval_cached_count").Int(),
			}), true
		}
	}
	return Usage{}, false
}
