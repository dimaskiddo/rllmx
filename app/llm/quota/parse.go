package quota

import (
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// maxBuckets bounds how many buckets one answer may add, so a hostile or broken
// answer cannot grow the cache.
const maxBuckets = 256

// importantModels are the models whose quota is kept; the answer lists many more.
var importantModels = map[string]bool{
	"gemini-4-argon-high": true, "gemini-4-argon-medium": true, "gemini-4-argon-low": true,
	"gemini-3.8-flash-high": true, "gemini-3.8-flash-medium": true, "gemini-3.8-flash-low": true,
	"gemini-3.7-flash-high": true, "gemini-3.7-flash-medium": true, "gemini-3.7-flash-low": true,
	"gemini-3.6-flash-high": true, "gemini-3.6-flash-medium": true, "gemini-3.6-flash-low": true,
	"gemini-3.1-pro-high": true, "gemini-pro-agent": true, "gemini-3.1-pro-low": true,
	"claude-sonnet-5-5-high": true, "claude-sonnet-5-5-medium": true, "claude-sonnet-5-5-low": true, "claude-sonnet-5-5": true,
	"claude-opus-5-5-high": true, "claude-opus-5-5-medium": true, "claude-opus-5-5-low": true, "claude-opus-5-5": true,
	"claude-sonnet-4-6": true, "claude-opus-4-6-thinking": true, "gpt-oss-120b-medium": true,
	"gemini-3.1-flash-image": true,
}

// ParseModels reads the per-model quota of a paid account from the answer to
// fetchAvailableModels. Free-tier accounts have only the weekly limit, so the
// caller does not call this for them.
func ParseModels(raw []byte) Snapshot {
	out := Snapshot{}
	models := gjson.GetBytes(raw, "models")
	if !models.IsObject() {
		return out
	}
	models.ForEach(func(name, info gjson.Result) bool {
		if len(out) >= maxBuckets {
			return false
		}
		q := info.Get("quotaInfo")
		if !q.IsObject() || info.Get("isInternal").Bool() || !importantModels[name.String()] {
			return true
		}
		out[name.String()] = Bucket{Remaining: percent(q.Get("remainingFraction")), ResetAt: ParseReset(q.Get("resetTime"))}
		return true
	})
	return out
}

// groupFamilies maps the display name of a quota group to its pool.
var groupFamilies = []struct {
	match   string
	session string
	weekly  string
}{
	// Argon must match before "gemini": its group name carries both words.
	{"argon", ArgonSession, ArgonWeekly},
	{"gemini", GeminiSession, GeminiWeekly},
	{"claude", ClaudeGPTSession, ClaudeGPTWeekly},
	{"gpt", ClaudeGPTSession, ClaudeGPTWeekly},
}

// ParseSummary reads the 5-hour and weekly buckets from the answer to
// retrieveUserQuotaSummary. A group may sit at `groups` or `quotaSummary.groups`.
func ParseSummary(raw []byte) Snapshot {
	out := Snapshot{}
	groups := gjson.GetBytes(raw, "groups")
	if !groups.IsArray() {
		groups = gjson.GetBytes(raw, "quotaSummary.groups")
	}
	if !groups.IsArray() {
		return out
	}
	for _, g := range groups.Array() {
		name := strings.ToLower(g.Get("displayName").String())
		for _, b := range g.Get("buckets").Array() {
			window := strings.ToLower(b.Get("window").String())
			text := strings.ToLower(b.Get("bucketId").String() + " " + b.Get("displayName").String())
			weekly := window == "weekly" || strings.Contains(text, "weekly")
			session := window == "5h" || window == "daily" || strings.Contains(text, "five hour") || strings.Contains(text, "5h") || strings.Contains(text, "daily")
			if !weekly && !session {
				continue
			}
			disabled := b.Get("disabled").Bool()
			if disabled && weekly {
				continue
			}
			frac := b.Get("remainingFraction")
			if disabled {
				frac = gjson.Result{Type: gjson.Number, Num: 0}
			} else if frac.Type != gjson.Number {
				continue
			}
			for _, f := range groupFamilies {
				if !strings.Contains(name, f.match) {
					continue
				}
				key := f.session
				if weekly {
					key = f.weekly
				}
				if _, have := out[key]; !have {
					out[key] = Bucket{Remaining: percent(frac), ResetAt: ParseReset(b.Get("resetTime"))}
				}
				break
			}
		}
	}
	return out
}

// percent turns a fraction into a percentage clamped to 0 to 100.
func percent(v gjson.Result) float64 {
	f := v.Float() * 100
	switch {
	case f < 0:
		return 0
	case f > 100:
		return 100
	}
	return f
}

// ParseReset reads a reset time given as a date, a Unix time in seconds or
// milliseconds, or the same as digits in a string. Anything else is the zero time.
func ParseReset(v gjson.Result) time.Time {
	switch v.Type {
	case gjson.Number:
		return unix(v.Int())
	case gjson.String:
		s := strings.TrimSpace(v.String())
		if s == "" {
			return time.Time{}
		}
		if digits(s) {
			return unix(v.Int())
		}
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

func digits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// unix reads seconds or, above 1e12, milliseconds.
func unix(n int64) time.Time {
	if n <= 0 {
		return time.Time{}
	}
	if n < 1_000_000_000_000 {
		return time.Unix(n, 0)
	}
	return time.UnixMilli(n)
}

// Reconcile marks a family's short window spent when every model of the family is
// spent, even though the summary still shows room: the per-model answer is the
// fresher one. The reset it takes is the latest among the spent models.
func Reconcile(models, summary Snapshot) Snapshot {
	for _, fam := range []struct {
		prefix, session string
		argon           bool
	}{{"gemini-", ArgonSession, true}, {"gemini-", GeminiSession, false}, {"claude-", ClaudeGPTSession, false}} {
		var count int
		var latest time.Time
		all := true
		for name, b := range models {
			if !strings.HasPrefix(name, fam.prefix) {
				continue
			}
			// A Gemini model belongs to exactly one family here: Argon's models
			// are counted only for Argon, and an image model for neither.
			argon := strings.Contains(name, "argon")
			if argon != fam.argon || (!argon && strings.Contains(name, "image")) {
				continue
			}
			count++
			if b.Remaining > 0 {
				all = false
			}
			if b.ResetAt.After(latest) {
				latest = b.ResetAt
			}
		}
		if cur, ok := summary[fam.session]; ok && count > 0 && all && cur.Remaining > 0 {
			cur.Remaining = 0
			if !latest.IsZero() {
				cur.ResetAt = latest
			}
			summary[fam.session] = cur
		}
	}
	return summary
}
