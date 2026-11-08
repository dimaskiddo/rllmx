package account

import (
	"fmt"
	"strings"
	"time"

	"rllmx/app/dialect"
	"rllmx/app/document"
	"rllmx/app/repository"
)

// lockUntil reads a lock field. A missing, null, non-string or unparsable value
// is no lock.
func lockUntil(d document.Doc, key string) (time.Time, bool) {
	v, ok := d.GetString(key)
	if !ok {
		return time.Time{}, false
	}
	t, err := dialect.ParseTimestamp(v)
	return t, err == nil
}

func lockKeys(model string) []string {
	if model == "" {
		return []string{repository.LockKey("")}
	}
	return []string{repository.LockKey(model), repository.LockKey("")}
}

// LockActive reports whether model is locked at now, by its own lock or by the
// account-level one. A lock that ends exactly now has ended.
func LockActive(d document.Doc, model string, now time.Time) bool {
	for _, key := range lockKeys(model) {
		if until, ok := lockUntil(d, key); ok && until.After(now) {
			return true
		}
	}
	return false
}

// EarliestLockFor is the first moment a lock that applies to model ends.
func EarliestLockFor(d document.Doc, model string, now time.Time) (time.Time, bool) {
	var first time.Time
	found := false
	for _, key := range lockKeys(model) {
		if until, ok := lockUntil(d, key); ok && until.After(now) && (!found || until.Before(first)) {
			first, found = until, true
		}
	}
	return first, found
}

// FormatRetryAfter renders the wait until a lock ends, rounded up to whole
// seconds: "reset after 2m 30s". A zero time gives an empty string.
func FormatRetryAfter(until, now time.Time) string {
	if until.IsZero() {
		return ""
	}
	total := int64((max(until.Sub(now), 0) + time.Second - 1) / time.Second)
	h, m, s := total/3600, total%3600/60, total%60
	var parts []string
	if h > 0 {
		parts = append(parts, fmt.Sprintf("%dh", h))
	}
	if m > 0 {
		parts = append(parts, fmt.Sprintf("%dm", m))
	}
	if s > 0 || len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("%ds", s))
	}
	return "reset after " + strings.Join(parts, " ")
}
