package pipeline

import (
	"rllmx/app/translate"
	"rllmx/app/upstream"
)

// setUsage puts the upstream's own counts and their price in the row. The
// client buffer is never added here.
func (c *call) setUsage(u translate.Usage) {
	r := &c.row
	r.PromptTokens, r.CompletionTokens = u.Prompt, u.Completion
	r.CachedTokens, r.CacheWriteTokens, r.ReasoningTokens = u.Cached, u.CacheWrite, u.Reasoning
	r.Estimated = u.Estimated
	if c.p.d.Prices == nil {
		return
	}
	r.Cost, _ = c.p.d.Prices.Cost(c.dest.provider, c.dest.model, upstream.Usage{
		Prompt: u.Prompt, Completion: u.Completion, Cached: u.Cached, CacheCreation: u.CacheWrite, Reasoning: u.Reasoning,
	})
}

// finish enqueues the one row the request leaves.
func (c *call) finish(out Output) {
	r := &c.row
	r.Status = out.Status
	if c.rowStatus != 0 {
		r.Status = c.rowStatus
	}
	r.ErrorClass = c.errClass
	if r.ErrorClass == "" {
		r.ErrorClass = classOf(out.Status)
	}
	r.LatencyMs = c.p.d.Now().Sub(c.start).Milliseconds()
	c.p.d.Usage.Enqueue(*r)
}
