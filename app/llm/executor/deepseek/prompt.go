package deepseek

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

// imageLink strips a markdown image the endpoint cannot fetch.
var imageLink = regexp.MustCompile(`!\[.*?\]\(.*?\)`)

// autoHistoryWindow is what the reference stitches in when the account names no
// window and the conversation holds more than one turn.
const autoHistoryWindow = 20

// options are what the web endpoint reads off the model name and the body.
type options struct {
	modelType string
	thinking  bool
	search    bool
}

// optionsOf derives the model type and the two switches the reference reads
// from the model name, then from the body.
func optionsOf(model string, body gjson.Result) options {
	m := strings.ToLower(model)
	o := options{modelType: "default"}
	if strings.Contains(m, "pro") || strings.Contains(m, "expert") {
		o.modelType = "expert"
	}
	o.thinking = strings.Contains(m, "r1") || strings.Contains(m, "think") || strings.Contains(m, "reason") ||
		flag(body, "thinking_enabled") || flag(body, "thinking") || body.Get("reasoning_effort").Exists()
	o.search = strings.Contains(m, "search") || strings.Contains(m, "fold") ||
		flag(body, "search_enabled") || flag(body, "search") || flag(body, "web_search")
	return o
}

// flag reads a body field the reference accepts in several shapes: true, a
// non-empty string, or any non-zero number.
func flag(body gjson.Result, name string) bool {
	v := body.Get(name)
	switch v.Type {
	case gjson.True:
		return true
	case gjson.String:
		return v.String() != ""
	case gjson.Number:
		return v.Num != 0
	}
	return false
}

// prompt flattens a conversation into the one string the web endpoint takes:
// the system text, then the last turns as a role-tagged transcript. window is
// the account's setting; 0 lets the reference's default apply.
func prompt(messages []gjson.Result, window int) string {
	var system, conversation []string
	names := map[string]string{}
	lastUser := ""
	for _, m := range messages {
		text := strings.TrimSpace(messageText(m.Get("content")))
		switch m.Get("role").String() {
		case "system":
			if text != "" {
				system = append(system, text)
			}
		case "user", "assistant":
			if text != "" {
				conversation = append(conversation, m.Get("role").String()+": "+text)
			}
			if m.Get("role").String() == "user" {
				lastUser = text
			}
			for _, c := range m.Get("tool_calls").Array() {
				if id := c.Get("id").String(); id != "" {
					names[id] = c.Get("function.name").String()
				}
			}
		case "tool":
			if text == "" {
				continue
			}
			name := m.Get("name").String()
			if id := m.Get("tool_call_id").String(); names[id] != "" {
				name = names[id]
			}
			if name == "" {
				name = "tool"
			}
			conversation = append(conversation, "Tool result ("+name+") "+text)
		}
	}
	effective := window
	if effective <= 0 && len(conversation) > 1 {
		effective = autoHistoryWindow
	}
	var parts []string
	if len(system) > 0 {
		parts = append(parts, strings.Join(system, "\n\n"))
	}
	switch {
	case effective > 0 && len(conversation) > 1:
		start := max(len(conversation)-effective, 0)
		parts = append(parts, strings.Join(conversation[start:], "\n\n"))
	case lastUser != "":
		parts = append(parts, lastUser)
	}
	return imageLink.ReplaceAllString(strings.Join(parts, "\n\n"), "")
}

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

// intField reads a non-negative integer setting from the account's data.
func intField(req func(string) string, name string) int {
	n, err := strconv.Atoi(strings.TrimSpace(req(name)))
	if err != nil || n < 0 {
		return 0
	}
	return n
}
