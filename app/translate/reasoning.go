package translate

import "rllmx/app/upstream"

// reasoningPlaceholder satisfies an upstream that demands reasoning text on
// every assistant turn of a thinking conversation. A client in OpenAI format
// never sends any.
const reasoningPlaceholder = " "

// reasoningScope is the injection scope for this request: the provider's own
// setting, else the one the model family needs.
func reasoningScope(c *Ctx) string {
	if s := c.Entry.Transport.ReasoningInject; s != "" {
		return s
	}
	return upstream.ModelReasoningScope(c.Model)
}

// injectReasoning puts the placeholder on an assistant message that has no
// reasoning text. "toolCalls" limits it to turns that called a tool.
func injectReasoning(w obj, m Message, scope string) {
	if scope == "" || m.Role != RoleAssistant || m.Reasoning != "" {
		return
	}
	if scope == upstream.ReasoningToolCalls && len(m.ToolCalls) == 0 {
		return
	}
	w["reasoning_content"] = reasoningPlaceholder
}
