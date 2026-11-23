package pipeline

import (
	"fmt"

	"rllmx/app/llm/translate"
	"rllmx/app/llm/upstream"
	"rllmx/app/platform/failure"
	"rllmx/app/store/analytics"
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
	c.markSkills(out.Status)
	r := &c.row
	r.Status = out.Status
	if c.rowStatus != 0 {
		r.Status = c.rowStatus
	}
	r.ErrorClass = c.errClass
	if r.ErrorClass == "" {
		r.ErrorClass = failure.ClassOf(out.Status)
	}
	r.LatencyMs = c.p.d.Now().Sub(c.start).Milliseconds()
	r.Savers, r.Skills = c.saverStats.Names(), c.skillStats.Names()
	// One line names what changed the request, so the console log answers
	// "what acted on this call" without printing a line for every request.
	if answerLogged(*r) && c.p.d.Log != nil {
		c.p.d.Log.Infof("%s", answerLine(*r))
	}
	c.p.d.Usage.Enqueue(*r)
}

// answerLine is the per-request log line for a call a saver or a skill changed.
// Names only: no prompt text, no address.
func answerLine(r analytics.Record) string {
	return fmt.Sprintf("answer id=%s provider=%s model=%s conn=%s attempt=%d status=%d ms=%d savers=%s skills=%s",
		r.RequestID, r.Provider, r.Model, r.ConnectionID, r.Attempt, r.Status, r.LatencyMs, r.Savers, r.Skills)
}

// answerLogged reports whether a request earned the feature line.
func answerLogged(r analytics.Record) bool { return r.Savers != "" || r.Skills != "" }
