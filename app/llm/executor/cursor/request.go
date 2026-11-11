package cursor

import (
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/store/dialect"
)

// A run request as the service reads it. The state is empty on purpose: it
// starts a fresh agent session, and a session this router cannot keep must not
// claim to be one the service remembers.
func runRequest(body []byte, model, frameMessageID string) ([]byte, error) {
	g := gjson.ParseBytes(body)
	user, history, err := conversation(g.Get("messages"))
	if err != nil {
		return nil, err
	}
	userMessage := join(
		text(userText, user),
		text(userID, frameMessageID),
		message(userCtx, nil),
		number(userMode, 1),
	)
	action := join(
		message(actionUser, userMessage),
		message(actionHistory, history),
	)
	details := join(text(modelName, model), text(modelDetailAlt, model), text(modelDetailTwo, model))
	request := join(
		message(runState, nil),
		message(runConversation, message(actionUser, action)),
		message(runModelDetails, details),
	)
	if tools := mcpTools(g.Get("tools")); len(tools) > 0 {
		request = append(request, message(runMCPTools, tools)...)
	}
	request = append(request, message(runRequested, requested(model, g.Get("reasoning_effort").String()))...)
	return frameBytes(message(clientRunRequest, request)), nil
}

// conversation splits the body's messages into the current user turn and the
// history before it, as the service's own client does. The system text rides on
// the current turn, because the service answers a system field with an empty
// turn.
func conversation(messages gjson.Result) (string, []byte, error) {
	var system []string
	var chat []gjson.Result
	for _, m := range messages.Array() {
		if m.Get("role").String() == "system" {
			if t := messageText(m.Get("content")); t != "" {
				system = append(system, t)
			}
			continue
		}
		chat = append(chat, m)
	}
	if len(chat) == 0 {
		return "", nil, errMessage
	}
	current := len(chat) - 1
	for i := len(chat) - 1; i >= 0; i-- {
		if chat[i].Get("role").String() == "user" {
			current = i
			break
		}
	}
	var history []byte
	for _, m := range chat[:current] {
		entry := historyEntry(m)
		if len(entry) > 0 {
			history = append(history, message(actionUser, entry)...)
		}
	}
	user := messageText(chat[current].Get("content"))
	if len(system) > 0 {
		user = strings.Join(system, "\n\n") + "\n\n" + user
	}
	if user == "" {
		user = "Continue."
	}
	return user, history, nil
}

// historyEntry writes one earlier turn. The service takes text only, so a call
// and a result become annotation lines rather than structured parts.
func historyEntry(m gjson.Result) []byte {
	var lines []string
	if t := messageText(m.Get("content")); t != "" {
		lines = append(lines, t)
	}
	for _, tc := range m.Get("tool_calls").Array() {
		lines = append(lines, "[tool_call id="+tc.Get("id").String()+" name="+tc.Get("function.name").String()+
			" args="+tc.Get("function.arguments").String()+"]")
	}
	if m.Get("role").String() == "tool" {
		lines = append(lines, "[tool_result id="+m.Get("tool_call_id").String()+"]")
	}
	if len(lines) == 0 {
		return nil
	}
	body := message(historyContent, text(historyContent, strings.Join(lines, "\n")))
	if m.Get("role").String() == "assistant" {
		return message(historyAssistant, body)
	}
	return message(historyUser, body)
}

// requested is the model the run asks for, with the effort as a named
// parameter. The service's own tool names the effort "reasoning" for a GPT
// model and "effort" for everything else.
func requested(model, effort string) []byte {
	request := join(text(modelName, model), number(requestedFlag, 1))
	if e := effortValue(effort); e != "" {
		name := "effort"
		if strings.HasPrefix(strings.ToLower(model), "gpt-") {
			name = "reasoning"
		}
		request = append(request, message(requestedParam, join(text(paramName, name), text(paramValue, e)))...)
	}
	return request
}

// effortValue maps a client's effort word to the service's, or "" for none.
func effortValue(effort string) string {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "", "none":
		return ""
	case "ultra":
		return "max"
	case "minimal":
		return "low"
	}
	return strings.ToLower(strings.TrimSpace(effort))
}

// mcpTools writes the tools the model may call. The service takes them here,
// not as a chat field, and answers with a call in its own message.
func mcpTools(tools gjson.Result) []byte {
	var out []byte
	for _, t := range tools.Array() {
		name := t.Get("function.name").String()
		if name == "" {
			continue
		}
		definition := join(
			text(toolName, name),
			text(toolDescription, t.Get("function.description").String()),
			message(toolSchema, encodeValue(json.RawMessage(t.Get("function.parameters").Raw))),
			text(toolProvider, toolProviderName),
			text(toolWireName, name),
		)
		// Each definition is one entry at field one of the tools message, which
		// the run request carries once at its own field.
		out = append(out, message(mcpToolEntry, definition)...)
	}
	return out
}

// toolProviderName is what this router calls itself in a tool definition.
const toolProviderName = "rllmx"

// mcpToolEntry is the field one definition repeats under.
const mcpToolEntry = 1

// messageText reads a message's text from a string or from its text parts.
func messageText(v gjson.Result) string {
	if v.Type == gjson.String {
		return v.String()
	}
	var parts []string
	for _, p := range v.Array() {
		if t := p.Get("text").String(); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "")
}

// newMessageID is the id the user turn carries.
func newMessageID() string { return dialect.NewID() }
