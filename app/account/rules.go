package account

import (
	"strings"
	"time"
)

const (
	maxScanned     = 4096
	githubProvider = "github"
	githubMonthly  = "you've reached your additional usage limit for your plan"
	antigravity    = "antigravity"
)

// Rule maps an error to a cooldown by a lower-case Text substring or an HTTP
// Status. A Limit rule reads text only on statuses that can mean a limited
// account, because on any other 4xx the text may echo the client's own input.
type Rule struct {
	Provider string
	Text     string
	Status   int
	Backoff  bool
	Limit    bool
	Cooldown time.Duration
}

// defaultRules is checked top to bottom: text rules first, then status rules.
var defaultRules = []Rule{
	{Provider: "codex", Text: "model is not supported when using codex with a chatgpt account", Cooldown: maxReported},
	{Provider: "codebuddy-intl", Text: "request illegal", Backoff: true},
	{Provider: "codebuddy-intl", Text: "conversation id missing", Backoff: true},
	{Text: "no credentials", Cooldown: longCooldown},
	{Text: "request not allowed", Cooldown: shortCooldown},
	{Text: "improperly formed request", Cooldown: longCooldown},
	{Text: "rate limit", Backoff: true, Limit: true},
	{Text: "too many requests", Backoff: true, Limit: true},
	{Text: "quota exceeded", Backoff: true, Limit: true},
	{Text: "insufficient credit", Backoff: true},
	{Text: "credit balance is too low", Backoff: true},
	{Text: "capacity", Backoff: true, Limit: true},
	{Text: "overloaded", Backoff: true, Limit: true},
	{Status: 401, Cooldown: longCooldown},
	{Status: 402, Cooldown: longCooldown},
	{Status: 403, Cooldown: longCooldown},
	{Status: 404, Cooldown: longCooldown},
	{Status: 429, Backoff: true},
}

// Input describes one failed upstream call.
type Input struct {
	Status   int
	Text     string
	Provider string
	// Level is the backoff level stored on the connection.
	Level int
	// ResetAt is when the provider says its limit ends, or the zero time.
	ResetAt time.Time
	// Now is the clock; the zero value means time.Now().
	Now time.Time
}

// Decision is how one failure affects the connection.
type Decision struct {
	// Fallback means the credential is at fault: lock it and try another.
	Fallback bool
	// RequestScoped means this request was rejected for its own sake: the
	// connection stays healthy, but a combo may try its next member.
	RequestScoped bool
	// AccountWide means the lock covers every model of the connection.
	AccountWide bool
	Cooldown    time.Duration
	// NextLevel is the backoff level to store. Only a backoff rule raises it, so
	// a 401 or a 5xx between two 429s does not restart their backoff.
	NextLevel int
}

// Classify judges a failure with defaultRules.
func Classify(in Input) Decision { return classify(defaultRules, in) }

func classify(rules []Rule, in Input) Decision {
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	text := in.Text
	if len(text) > maxScanned {
		text = text[:maxScanned]
	}
	text = strings.ToLower(text)

	if in.Provider == githubProvider && in.Status == 402 && strings.Contains(text, githubMonthly) {
		u := now.UTC()
		first := time.Date(u.Year(), u.Month()+1, 1, 0, 0, 0, 0, time.UTC)
		return Decision{Fallback: true, AccountWide: true, Cooldown: first.Sub(now)}
	}

	echoable := in.Status >= 400 && in.Status < 500 && in.Status != 401 && in.Status != 402 && in.Status != 403 && in.Status != 429
	for _, r := range rules {
		if r.Provider != "" && r.Provider != in.Provider {
			continue
		}
		if r.Limit && echoable {
			continue
		}
		if (r.Text != "" && strings.Contains(text, r.Text)) || (r.Status != 0 && r.Status == in.Status) {
			return reported(matched(r, in), in, now)
		}
	}

	if in.Status >= 400 && in.Status < 500 {
		return Decision{RequestScoped: true}
	}
	return reported(Decision{Fallback: true, Cooldown: transient, NextLevel: in.Level}, in, now)
}

func matched(r Rule, in Input) Decision {
	if r.Backoff {
		next := NextLevel(in.Level)
		return Decision{Fallback: true, Cooldown: Cooldown(next), NextLevel: next}
	}
	return Decision{Fallback: true, Cooldown: r.Cooldown, NextLevel: in.Level}
}

// reported lets a reset time the provider gave replace a cooldown that is
// already decided. A past or present reset changes nothing, and a request-scoped
// error stays unlocked whatever the provider says. Antigravity reports exact
// per-model times, so its reset is not cut short.
func reported(d Decision, in Input, now time.Time) Decision {
	if !d.Fallback || in.ResetAt.IsZero() || !in.ResetAt.After(now) {
		return d
	}
	d.Cooldown = in.ResetAt.Sub(now)
	if in.Provider != antigravity {
		d.Cooldown = min(d.Cooldown, maxReported)
	}
	d.NextLevel = 0
	return d
}
