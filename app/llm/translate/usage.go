package translate

// NormalizeUsage enforces the invariants every consumer relies on: a negative
// count is zero, the cached and cache-write shares never exceed the prompt
// they are part of, and reasoning never exceeds the completion.
func NormalizeUsage(u Usage) Usage {
	u.Prompt = max(u.Prompt, 0)
	u.Completion = max(u.Completion, 0)
	u.Cached = min(max(u.Cached, 0), u.Prompt)
	u.CacheWrite = min(max(u.CacheWrite, 0), u.Prompt-u.Cached)
	u.Reasoning = min(max(u.Reasoning, 0), u.Completion)
	return u
}
