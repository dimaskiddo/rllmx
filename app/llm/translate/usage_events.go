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
			return ReadOpenAIUsage(u), true
		}
	case FormatClaude:
		switch get("type").String() {
		case "message_start":
			if u := get("message.usage"); u.IsObject() {
				return ReadClaudeUsage(u), true
			}
		case "message_delta":
			if u := get("usage"); u.IsObject() {
				return ReadClaudeUsage(u), true
			}
		}
	case FormatResponses:
		if u := get("response.usage"); u.IsObject() {
			return ReadResponsesUsage(u), true
		}
		if u := get("usage"); u.IsObject() && get("type").String() != "" {
			return ReadResponsesUsage(u), true
		}
	case FormatGemini, FormatCloudCode, FormatVertex:
		for _, p := range []string{"usageMetadata", "response.usageMetadata"} {
			if u := get(p); u.IsObject() {
				return ReadGeminiUsage(u), true
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

// ReadClaudeUsage reads Claude's usage, whose input count excludes the cache:
// the prompt is the input plus what was read from and written to the cache.
func ReadClaudeUsage(u gjson.Result) Usage {
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

// ReadGeminiUsage reads Gemini's usage, where the prompt already includes the
// cached tokens and the completion is the candidates plus the thought tokens.
func ReadGeminiUsage(u gjson.Result) Usage {
	if !u.Exists() {
		return Usage{}
	}
	thoughts := u.Get("thoughtsTokenCount").Int()
	cand := u.Get("candidatesTokenCount").Int()
	if !u.Get("candidatesTokenCount").Exists() && u.Get("totalTokenCount").Exists() {
		cand = max(u.Get("totalTokenCount").Int()-u.Get("promptTokenCount").Int()-thoughts, 0)
	}
	return NormalizeUsage(Usage{
		Prompt: u.Get("promptTokenCount").Int(), Completion: cand + thoughts,
		Cached: u.Get("cachedContentTokenCount").Int(), Reasoning: thoughts,
	})
}

func ReadResponsesUsage(u gjson.Result) Usage {
	if !u.Exists() {
		return Usage{}
	}
	return NormalizeUsage(Usage{
		Prompt: u.Get("input_tokens").Int(), Completion: u.Get("output_tokens").Int(),
		Cached:    u.Get("input_tokens_details.cached_tokens").Int(),
		Reasoning: u.Get("output_tokens_details.reasoning_tokens").Int(),
	})
}

func ReadOpenAIUsage(u gjson.Result) Usage {
	if !u.Exists() {
		return Usage{}
	}
	out := Usage{
		Prompt: u.Get("prompt_tokens").Int(), Completion: u.Get("completion_tokens").Int(),
		Reasoning:  u.Get("completion_tokens_details.reasoning_tokens").Int(),
		Cached:     FirstPositive(u.Get("prompt_tokens_details.cached_tokens").Int(), u.Get("prompt_cache_hit_tokens").Int()),
		CacheWrite: u.Get("prompt_tokens_details.cache_creation_tokens").Int(),
	}
	return NormalizeUsage(out)
}

func FirstPositive(vals ...int64) int64 {
	for _, v := range vals {
		if v > 0 {
			return v
		}
	}
	return 0
}
