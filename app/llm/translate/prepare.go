package translate

import "rllmx/app/llm/upstream"

// Prepare applies the concerns that hold for every upstream, in the order the
// rest depends on: thinking first (it decides token headroom), then what the
// model cannot read, then the shape of tool calls. Format-specific preparation
// (Claude cache markers, Gemini turn order) happens inside the codec that
// writes that format.
func Prepare(r *Request, c *Ctx) {
	// A field the hub does not model belongs to the format it came from; written
	// into another one it is at best ignored and at worst refused.
	if c.ClientFormat != c.TargetFormat {
		r.Extra = nil
	}
	if c.Thinking.Set() {
		r.Thinking = c.Thinking
	}
	DropThinkingForPrefill(r)
	StripUnreadable(r, c.Caps)
	EnsureToolIDs(r)
	FixMissingToolResults(r)
	AdjustMaxTokens(r, c.Caps)
}

// Finish writes the final upstream body: the parameter rules run last, on the
// bytes, so they see everything an injection added.
func Finish(body []byte, rules []upstream.ParamRule) []byte { return ApplyParams(body, rules) }
