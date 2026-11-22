package translate

import (
	"slices"
	"strings"
)

// systemSep separates one injected prompt from the text around it, and is how a
// second run recognises its own earlier work.
const systemSep = "\n\n"

// LastUserText is the text of the last user turn that holds any, trimmed and
// uncut. Each caller cuts it to the bound its own scoring needs.
func (r *Request) LastUserText() string {
	if r == nil {
		return ""
	}
	for _, m := range slices.Backward(r.Messages) {
		if m.Role != RoleUser {
			continue
		}
		if t := strings.TrimSpace(m.Text()); t != "" {
			return t
		}
	}
	return ""
}

// AppendSystem adds a prompt to the first system turn, or puts a new system turn
// at the top when there is none. It reports whether the request changed: a
// prompt already present as a segment of that turn is not added again, so an
// account retry or a rebuilt body never doubles it.
func (r *Request) AppendSystem(prompt string) bool {
	if prompt == "" {
		return false
	}
	for i := range r.Messages {
		if r.Messages[i].Role != RoleSystem {
			continue
		}
		msg := &r.Messages[i]
		body := msg.Text()
		if hasSegment(body, prompt) {
			return false
		}
		if body == "" {
			msg.Content = TextPart(prompt)
			return true
		}
		msg.Content = append(msg.Content, Part{Type: PartText, Text: systemSep + prompt})
		return true
	}
	r.Messages = append([]Message{{Role: RoleSystem, Content: TextPart(prompt)}}, r.Messages...)
	return true
}

// hasSegment reports the prompt as a whole segment of the text, so unrelated text
// that happens to contain it is left alone.
func hasSegment(body, target string) bool {
	if body == target {
		return true
	}
	for segment := range strings.SplitSeq(body, systemSep) {
		if segment == target {
			return true
		}
	}
	return false
}
