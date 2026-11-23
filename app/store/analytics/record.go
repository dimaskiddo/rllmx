package analytics

import (
	"math"
	"strings"
	"time"

	"rllmx/app/platform/text"
)

// Record is one completed request. No field of it can hold a body, a header or
// prompt text: ErrorClass is a short code, never the upstream message.
type Record struct {
	Time         time.Time
	RequestID    string
	APIKeyID     string
	APIKeyName   string
	ConnectionID string
	Provider     string
	Model        string
	Endpoint     string
	SourceFormat string
	Status       int
	ErrorClass   string
	Stream       bool
	Cached       bool
	// Estimated marks token counts the gateway guessed because the provider
	// reported none.
	Estimated bool

	PromptTokens     int64
	CompletionTokens int64
	CachedTokens     int64
	CacheWriteTokens int64
	ReasoningTokens  int64
	Cost             float64
	LatencyMs        int64
	TTFBMs           int64
	ComboName        string
	// Savers and Skills name what changed the request, never its text.
	Savers  string
	Skills  string
	Attempt int
}

// row is a Record on its way into the database; importID is set only by the
// legacy importer, which needs the uniqueness that makes a rerun harmless.
type row struct {
	Record
	importID *string
}

const (
	maxKeyName  = 64
	maxModel    = 128
	maxEndpoint = 256
	maxID       = 128
	maxFeatures = 256
)

var errorClasses = map[string]bool{
	"none": true, "auth": true, "access_denied": true, "key_quota": true, "rate_limit": true, "quota": true,
	"invalid_request": true, "not_found": true, "no_account": true, "upstream_5xx": true, "network": true,
	"timeout": true, "stream_stall": true, "client_closed": true, "unknown": true,
}

func clean(s string, limit int) string {
	return text.CutRunes(text.StripControl(s), limit)
}

// keyName strips control characters, trims and then bounds, in that order, so a
// name padded with invisible bytes cannot use up its 64 characters.
func keyName(s string) string {
	return text.CutRunes(strings.TrimSpace(text.StripControl(s)), maxKeyName)
}

func nonNeg(n int64) int64 {
	if n < 0 {
		return 0
	}
	return n
}

// sanitize bounds every free-form field and maps an unknown error class to
// "unknown". It is the only place a value from a request becomes a stored one.
func sanitize(r Record) Record {
	if r.Time.IsZero() {
		r.Time = time.Now()
	}
	r.Time = r.Time.UTC()
	r.APIKeyName = keyName(r.APIKeyName)
	r.Model = clean(r.Model, maxModel)
	r.Endpoint = clean(r.Endpoint, maxEndpoint)
	r.RequestID = clean(r.RequestID, maxID)
	r.APIKeyID = clean(r.APIKeyID, maxID)
	r.ConnectionID = clean(r.ConnectionID, maxID)
	r.Provider = clean(r.Provider, maxID)
	r.SourceFormat = clean(r.SourceFormat, maxID)
	r.ComboName = clean(r.ComboName, maxID)
	r.Savers = clean(r.Savers, maxFeatures)
	r.Skills = clean(r.Skills, maxFeatures)
	switch {
	case r.ErrorClass == "" && r.Status >= 400:
		r.ErrorClass = "unknown"
	case r.ErrorClass == "":
		r.ErrorClass = "none"
	case !errorClasses[r.ErrorClass]:
		r.ErrorClass = "unknown"
	}
	r.PromptTokens, r.CompletionTokens = nonNeg(r.PromptTokens), nonNeg(r.CompletionTokens)
	r.CachedTokens, r.CacheWriteTokens = nonNeg(r.CachedTokens), nonNeg(r.CacheWriteTokens)
	r.ReasoningTokens, r.LatencyMs, r.TTFBMs = nonNeg(r.ReasoningTokens), nonNeg(r.LatencyMs), nonNeg(r.TTFBMs)
	if math.IsNaN(r.Cost) || math.IsInf(r.Cost, 0) || r.Cost < 0 {
		r.Cost = 0
	}
	if r.Attempt < 0 {
		r.Attempt = 0
	}
	return r
}
