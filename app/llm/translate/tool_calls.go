package translate

import (
	"fmt"
	"regexp"
	"strings"
)

// placeholderResult is the content of a tool result added for a call nobody
// answered, so an upstream that requires every call to be answered accepts the
// history.
const placeholderResult = "[No response received]"

var toolIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// toolID makes an id a strict upstream accepts: letters, digits, dash and
// underscore. A usable id is kept; a damaged one is cleaned; one with nothing
// left is built from its place, so the same history always gets the same ids.
func toolID(id string, msg, call int, name string) string {
	if toolIDPattern.MatchString(id) {
		return id
	}
	var b strings.Builder
	for _, r := range id {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	if b.Len() > 0 {
		return b.String()
	}
	suffix := ""
	if clean := CleanName(name); clean != "" {
		suffix = "_" + clean
	}
	return fmt.Sprintf("call_msg%d_tc%d%s", msg, call, suffix)
}

func CleanName(n string) string {
	var b strings.Builder
	for _, r := range n {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// EnsureToolIDs gives every tool call a valid id and keeps each result paired
// with its call. An id that was rewritten is rewritten the same way in the
// result that answers it, and ids are unique over the whole history: two calls
// that arrive with one id get distinct ones.
func EnsureToolIDs(r *Request) {
	seen := map[string]bool{}
	// renamed maps an incoming id to the id its call now has, per call position,
	// so a result is paired with the call it follows.
	pending := map[string][]string{}
	for i := range r.Messages {
		m := &r.Messages[i]
		for j := range m.ToolCalls {
			tc := &m.ToolCalls[j]
			old := tc.ID
			id := toolID(old, i, j, tc.Name)
			for n := 2; seen[id]; n++ {
				id = fmt.Sprintf("%s_%d", toolID(old, i, j, tc.Name), n)
			}
			seen[id] = true
			tc.ID = id
			pending[old] = append(pending[old], id)
		}
		if m.Role == RoleTool {
			old := m.ToolCallID
			if ids := pending[old]; len(ids) > 0 {
				m.ToolCallID = ids[0]
				pending[old] = ids[1:]
			} else {
				m.ToolCallID = toolID(old, i, 0, "")
			}
		}
	}
}

// FixMissingToolResults adds a placeholder result after any assistant turn that
// called a tool nobody answered. Results are collected from the messages that
// directly follow the call, in any order.
func FixMissingToolResults(r *Request) {
	for i := 0; i < len(r.Messages); i++ {
		m := r.Messages[i]
		if m.Role != RoleAssistant || len(m.ToolCalls) == 0 {
			continue
		}
		answered := map[string]bool{}
		insert := i + 1
		for j := i + 1; j < len(r.Messages) && r.Messages[j].Role == RoleTool; j++ {
			answered[r.Messages[j].ToolCallID] = true
			insert = j + 1
		}
		var add []Message
		for _, tc := range m.ToolCalls {
			if !answered[tc.ID] {
				add = append(add, Message{Role: RoleTool, ToolCallID: tc.ID, Content: TextPart(placeholderResult)})
			}
		}
		if len(add) == 0 {
			continue
		}
		rest := append([]Message{}, r.Messages[insert:]...)
		r.Messages = append(append(r.Messages[:insert], add...), rest...)
		i = insert + len(add) - 1
	}
}
