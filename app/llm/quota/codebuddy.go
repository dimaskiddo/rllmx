package quota

import (
	"context"
	"net/http"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/upstream"
)

// codeBuddyURL is each region's billing address.
var codeBuddyURL = map[string]string{
	"codebuddy-cn":   "https://copilot.tencent.com/v2/billing/meter/get-user-resource",
	"codebuddy-intl": "https://www.codebuddy.ai/v2/billing/meter/get-user-resource",
}

// readCodeBuddy reads the account's credit packages. A login's token and a
// pasted key are the same credential there.
func readCodeBuddy(ctx context.Context, r *Reader, e upstream.Entry, a Access) (Reading, error) {
	target, ok := codeBuddyURL[a.Conn.Provider]
	if !ok {
		return Reading{}, ErrUnsupported
	}
	h := keyedFor(e, a)
	raw, stop, early, earlyErr := r.getOK(ctx, "CodeBuddy", request{Method: http.MethodPost, URL: target, Headers: h, Body: []byte(`{}`)})
	if stop {
		return early, earlyErr
	}
	return parseCodeBuddy(raw), nil
}

func parseCodeBuddy(raw []byte) Reading {
	out := Reading{Quotas: map[string]Quota{}, Plan: "CodeBuddy"}
	data := gjson.ParseBytes(raw)
	if code := data.Get("code"); code.Exists() && code.Int() != 0 {
		out.Message = "CodeBuddy refused the request for this account"
		return out
	}
	accounts := data.Get("data.Response.Data.Accounts").Array()
	if len(accounts) == 0 {
		out.Message = "CodeBuddy reported no credit package"
		return out
	}
	if name := data.Get("data.Response.Data.PackageName").String(); name != "" {
		out.Plan = name
	}
	seen := map[string]int{}
	bonus := 0
	for _, acc := range accounts {
		cycle := stampOf(acc.Get("CycleEndTime"))
		// A refill pack rolls into a new cycle before the resource expires; a bonus
		// pack ends exactly at its expiry.
		if until := acc.Get("DeductionEndTime").Int(); until > 0 && !cycle.IsZero() &&
			time.UnixMilli(until).Sub(cycle) > 48*time.Hour {
			label := codeBuddyCadence(cycle, acc)
			seen[label]++
			if seen[label] > 1 {
				label = label + " " + numberText(float64(seen[label]))
			}
			q := window(acc.Get("CycleCapacityUsedPrecise").Float(), acc.Get("CycleCapacitySizePrecise").Float(), cycle)
			q.DisplayName, q.Recurring = label, true
			out.Quotas[label] = q
			continue
		}
		bonus++
		label := "Bonus Pack " + numberText(float64(bonus))
		q := window(acc.Get("CapacityUsedPrecise").Float(), acc.Get("CapacitySizePrecise").Float(), cycle)
		q.DisplayName = label
		out.Quotas[label] = q
	}
	return out
}

// codeBuddyCadence names a refill pack by how long its cycle runs.
func codeBuddyCadence(cycle time.Time, acc gjson.Result) string {
	start := stampOf(acc.Get("CycleStartTime"))
	if start.IsZero() {
		return "Monthly"
	}
	days := cycle.Sub(start).Hours() / 24
	switch {
	case days <= 1.5:
		return "Daily"
	case days <= 10:
		return "Weekly"
	}
	return "Monthly"
}
