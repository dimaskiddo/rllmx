package executor

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// A final answer that says nothing: an ellipsis alone, or a promise to look
// into it next. Both leave a client with no answer, so the turn is asked again
// with an instruction to finish.
var (
	kiroEllipsis = regexp.MustCompile(`^(?:\.{2,}|…)$`)
	kiroFuture   = regexp.MustCompile(`(?i)^(?:(?:next|now|then)\b[\s,:-]*)?(?:i(?:'ll| will| am going to| need to)|let me)\s+(?:verify|check|confirm|validate|investigate|trace|continue|follow up|test)\b`)
)

// The instructions a repair adds to the last user turn. They say what was
// wrong, and never name the provider or the router.
const (
	kiroRepairTool = "Retry the previous response because its tool call was malformed. If you use the wrapper tool named tool_call, its input must contain a non-empty name and an arguments field."
	kiroRepairBare = "Retry the previous response because it ended with only an ellipsis. Return the complete final answer, not only ... or …."
	kiroRepairOpen = "Retry the previous response because its final only announced a future action. Complete the check now and return the result or a concrete blocker."
)

// kiroUnfinished reports an answer that carries no result.
func kiroUnfinished(text, reasoning string) bool {
	content := strings.TrimSpace(text)
	if kiroEllipsis.MatchString(content) {
		return true
	}
	if content == "" && kiroEllipsis.MatchString(strings.TrimSpace(reasoning)) {
		return true
	}
	return kiroFuture.MatchString(content)
}

// kiroRepairFor is the instruction an unfinished answer earns.
func kiroRepairFor(text, reasoning string) string {
	if kiroEllipsis.MatchString(strings.TrimSpace(text)) || (strings.TrimSpace(text) == "" && kiroEllipsis.MatchString(strings.TrimSpace(reasoning))) {
		return kiroRepairBare
	}
	return kiroRepairOpen
}

// gjsonValidObject reports a JSON object, which is what a tool's arguments must
// be for a client to parse them.
func gjsonValidObject(raw []byte) bool {
	return gjson.ValidBytes(raw) && gjson.ParseBytes(raw).IsObject()
}

// appendKiroRepair adds the instruction to the last user turn of a request
// body. It goes in the turn's content, never in a top-level field: the upstream
// refuses one of those.
func appendKiroRepair(body []byte, instruction string) []byte {
	if instruction == "" || !gjson.ValidBytes(body) {
		return body
	}
	path := "conversationState.currentMessage.userInputMessage.content"
	existing := gjson.GetBytes(body, path).String()
	updated, err := sjson.SetBytes(body, path, existing+"\n\n"+instruction)
	if err != nil {
		return body
	}
	return updated
}

// kiroFailureBody is the one event a failed turn is answered with. It carries a
// fixed reason, never the upstream's text.
func kiroFailureBody(reason string) string {
	if reason == "" {
		reason = "the upstream ended the turn without an answer"
	}
	out, err := json.Marshal(map[string]any{"error": map[string]any{"type": "api_error", "message": reason}})
	if err != nil {
		return `{"error":{"message":"the upstream ended the turn without an answer","type":"api_error"}}`
	}
	return "event: error\ndata: " + string(out) + "\n\n"
}
