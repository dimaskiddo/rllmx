package prompt

import (
	"strings"

	"rllmx/app/llm/translate"
)

// sep separates one injected prompt from the text around it, and is how a
// second run recognises its own earlier work.
const sep = "\n\n"

// Inject appends a prompt to the request's first system message, creating one at
// the top when there is none. It reports whether the request changed; a prompt
// that is already a segment is not added twice, so an account retry or a
// rebuilt body never doubles it.
func Inject(r *translate.Request, text string) bool {
	if text == "" {
		return false
	}
	for i := range r.Messages {
		if r.Messages[i].Role != translate.RoleSystem {
			continue
		}
		msg := &r.Messages[i]
		if hasSegment(msg.Text(), text) {
			return false
		}
		if msg.Text() == "" {
			msg.Content = translate.TextPart(text)
			return true
		}
		msg.Content = append(msg.Content, translate.Part{Type: translate.PartText, Text: sep + text})
		return true
	}
	r.Messages = append([]translate.Message{{
		Role:    translate.RoleSystem,
		Content: translate.TextPart(text),
	}}, r.Messages...)
	return true
}

// hasSegment reports the prompt as its own segment of the text, so unrelated
// text that happens to contain it is left alone.
func hasSegment(text, target string) bool {
	if text == target {
		return true
	}
	for segment := range strings.SplitSeq(text, sep) {
		if segment == target {
			return true
		}
	}
	return false
}
