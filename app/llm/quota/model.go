// Package quota keeps what Antigravity says is left of an account's usage and
// uses it to pass over an account whose model is spent. The numbers are fetched
// from Google lazily, after a refusal, and held in memory until the model's own
// reset time; nothing here is stored or sent to a client.
package quota

import (
	"strings"
	"time"
)

// Bucket is one measured limit: how much of it is left and when it refills.
type Bucket struct {
	// Remaining is the share left, 0 to 100.
	Remaining float64
	// ResetAt is when the limit refills, or the zero time when unknown.
	ResetAt time.Time
}

// Exhausted reports a bucket with nothing left that has not refilled yet.
func (b Bucket) Exhausted(now time.Time) bool {
	return b.Remaining <= 0 && b.ResetAt.After(now)
}

// Keys of the shared buckets beside the per-model ones. A family has a short
// window and a weekly one.
const (
	GeminiSession = "gemini_session"
	GeminiWeekly  = "gemini_weekly"
	// The Argon family is measured apart from the rest of the Gemini models and
	// checked before them.
	ArgonSession     = "gemini_argon_session"
	ArgonWeekly      = "gemini_argon_weekly"
	ClaudeGPTSession = "claude_gpt_session"
	ClaudeGPTWeekly  = "claude_gpt_weekly"
)

// Snapshot is the buckets of one account by name: a model id or a family key.
type Snapshot map[string]Bucket

// Family says which pool a model draws on: "claude_gpt", "argon", "gemini", or
// "" for a model that draws on neither (an image model has its own limit).
// Argon is checked first: its models are Gemini models with their own buckets.
func Family(model string) string {
	m := strings.ToLower(cleanModel(model))
	switch {
	case strings.HasPrefix(m, "claude-"), strings.HasPrefix(m, "gpt-"), m == "claude":
		return "claude_gpt"
	case strings.HasPrefix(m, "gemini-") && strings.Contains(m, "argon"):
		return "argon"
	case strings.HasPrefix(m, "gemini-") && !strings.Contains(m, "image"), m == "gemini":
		return "gemini"
	}
	return ""
}

// cleanModel drops a provider prefix and a trailing "(level)".
func cleanModel(model string) string {
	if i := strings.IndexByte(model, '/'); i >= 0 {
		model = model[i+1:]
	}
	if strings.HasSuffix(model, ")") {
		if i := strings.LastIndexByte(model, '('); i > 0 {
			model = model[:i]
		}
	}
	return strings.TrimSpace(model)
}

// tierAlias maps a base model to the tiers whose quota it may share, in the
// order they are tried: the first one the account's list has is the one used.
var tierAlias = map[string][]string{
	"claude-sonnet-5-5": {"claude-sonnet-5-5-medium", "claude-sonnet-5-5-high"},
	"claude-opus-5-5":   {"claude-opus-5-5-medium", "claude-opus-5-5-high"},
	"gemini-4-carbon":   {"gemini-4-carbon-high"},
	"gemini-4-argon":    {"gemini-4-argon-high"},
	"gemini-4-neon":     {"gemini-4-neon-high"},
	"gemini-4-hydrogen": {"gemini-4-hydrogen-high"},
	"gemini-3.8-flash":  {"gemini-3.8-flash-medium"},
	"gemini-3.7-flash":  {"gemini-3.7-flash-medium"},
	"gemini-3.6-flash":  {"gemini-3.6-flash-medium"},
}

// Of finds the bucket that decides whether a model can be used. Every bucket the
// model draws on is looked at: if any is spent, the model is spent, and the
// latest reset among the spent ones is kept so the account is not tried again too
// soon. With none spent, the tightest bucket is returned. ok is false when the
// snapshot says nothing about the model.
func (s Snapshot) Of(model string, now time.Time) (Bucket, bool) {
	clean := cleanModel(model)
	var candidates []Bucket
	add := func(key string) {
		if b, ok := s[key]; ok {
			candidates = append(candidates, b)
		}
	}
	add(clean)
	for _, alias := range tierAlias[clean] {
		if _, ok := s[alias]; ok {
			add(alias)
			break
		}
	}
	switch Family(clean) {
	case "claude_gpt":
		add(ClaudeGPTSession)
		add(ClaudeGPTWeekly)
	case "argon":
		add(ArgonSession)
		add(ArgonWeekly)
	case "gemini":
		add(GeminiSession)
		add(GeminiWeekly)
	}
	if len(candidates) == 0 {
		return Bucket{}, false
	}
	var spent *Bucket
	open := make([]Bucket, 0, len(candidates))
	for i := range candidates {
		c := candidates[i]
		if c.Remaining > 0 {
			open = append(open, c)
			continue
		}
		if !c.ResetAt.IsZero() && !c.ResetAt.After(now) {
			// Its reset has passed, so it has refilled; the number is stale.
			continue
		}
		if spent == nil || c.ResetAt.After(spent.ResetAt) {
			spent = &candidates[i]
		}
	}
	if spent != nil {
		return Bucket{Remaining: 0, ResetAt: spent.ResetAt}, true
	}
	if len(open) == 0 {
		return Bucket{Remaining: 100}, true
	}
	tight := open[0]
	for _, c := range open[1:] {
		if c.Remaining < tight.Remaining {
			tight = c
		}
	}
	return tight, true
}
