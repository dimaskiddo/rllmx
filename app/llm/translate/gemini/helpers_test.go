package gemini

import "rllmx/app/llm/translate"

// The three message builders a hub test needs.
func user(text string) translate.Message {
	return translate.Message{Role: translate.RoleUser, Content: translate.TextPart(text)}
}

func asst(calls ...translate.ToolCall) translate.Message {
	return translate.Message{Role: translate.RoleAssistant, ToolCalls: calls}
}

func result(id, text string) translate.Message {
	return translate.Message{Role: translate.RoleTool, ToolCallID: id, Content: translate.TextPart(text)}
}
