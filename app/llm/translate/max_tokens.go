package translate

import "rllmx/app/llm/upstream"

// minThinkingHeadroom is the room left for the answer above a thinking budget.
const minThinkingHeadroom = 1024

// AdjustMaxTokens cuts max_tokens to what the model can produce and keeps it
// above a thinking budget, which a Claude upstream requires. A request that
// names no limit is left alone.
func AdjustMaxTokens(r *Request, c upstream.Capability) {
	p := r.Params.MaxTokens
	if p == nil {
		return
	}
	n := *p
	if c.MaxOutput > 0 && n > c.MaxOutput {
		n = c.MaxOutput
	}
	if r.Thinking.Mode == "budget" && r.Thinking.Budget > 0 && n <= r.Thinking.Budget {
		n = r.Thinking.Budget + minThinkingHeadroom
		if c.MaxOutput > 0 && n > c.MaxOutput {
			n = c.MaxOutput
		}
	}
	*p = n
}
