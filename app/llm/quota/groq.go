package quota

import (
	"context"
	"net/http"
	"time"

	"rllmx/app/llm/upstream"
)

// groqModelsURL answers a model list; the rate limits ride on its headers.
const groqModelsURL = "https://api.groq.com/openai/v1/models"

// readGroq reads the rate limits Groq puts on the answer's headers. The body is
// drained and dropped: the list itself is not a quota.
func readGroq(ctx context.Context, r *Reader, e upstream.Entry, a Access) (Reading, error) {
	code, header, _, err := r.doFull(ctx, request{URL: groqModelsURL, Headers: keyedFor(e, a)})
	if err != nil {
		return failed("Groq", err)
	}
	if code != http.StatusOK {
		return answered("Groq", code)
	}
	out := Reading{Quotas: map[string]Quota{}, Plan: "Groq"}
	for _, w := range []struct{ label, limit, remaining, reset string }{
		{"Requests", "x-ratelimit-limit-requests", "x-ratelimit-remaining-requests", "x-ratelimit-reset-requests"},
		{"Tokens", "x-ratelimit-limit-tokens", "x-ratelimit-remaining-tokens", "x-ratelimit-reset-tokens"},
	} {
		total, left := header.Get(w.limit), header.Get(w.remaining)
		if total == "" || left == "" {
			continue
		}
		limit, ok1 := atof(total)
		remaining, ok2 := atof(left)
		if !ok1 || !ok2 {
			continue
		}
		row := window(limit-remaining, limit, durationFrom(header.Get(w.reset), r.now()))
		row.DisplayName, row.Recurring = w.label, true
		out.Quotas[w.label] = row
	}
	if len(out.Quotas) == 0 {
		out.Message = "Groq reported no quota"
	}
	return out, nil
}

// durationFrom reads a Go-style duration ("2m59.56s") as a reset time.
func durationFrom(s string, now time.Time) time.Time {
	if s == "" {
		return time.Time{}
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return time.Time{}
	}
	return now.Add(d)
}
