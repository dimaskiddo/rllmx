package translate

// FinishFromClaude maps a Claude stop reason to the hub's.
func FinishFromClaude(reason string) string {
	switch reason {
	case "end_turn", "stop_sequence", "":
		return FinishStop
	case "max_tokens":
		return FinishLength
	case "tool_use":
		return FinishToolCalls
	case "refusal":
		return FinishFilter
	}
	return FinishStop
}

// FinishToClaude maps the hub's finish reason to a Claude stop reason.
func FinishToClaude(reason string) string {
	switch reason {
	case FinishLength:
		return "max_tokens"
	case FinishToolCalls:
		return "tool_use"
	case FinishFilter:
		return "refusal"
	}
	return "end_turn"
}

// FinishFromGemini maps a Gemini finish reason to the hub's. The safety family
// of reasons is a content filter.
func FinishFromGemini(reason string) string {
	switch reason {
	case "STOP", "":
		return FinishStop
	case "MAX_TOKENS", "CONTINUATION":
		return FinishLength
	case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII", "IMAGE_SAFETY", "LANGUAGE":
		return FinishFilter
	}
	return FinishStop
}

// FinishToGemini maps the hub's finish reason to Gemini's.
func FinishToGemini(reason string) string {
	switch reason {
	case FinishLength:
		return "MAX_TOKENS"
	case FinishFilter:
		return "SAFETY"
	}
	return "STOP"
}

// FinishWithCalls turns a final stop into tool_calls when the turn emitted any,
// because several upstreams report "stop" after asking for a tool.
func FinishWithCalls(reason string, calls int) string {
	if calls > 0 && (reason == "" || reason == FinishStop) {
		return FinishToolCalls
	}
	if reason == "" {
		return FinishStop
	}
	return reason
}
