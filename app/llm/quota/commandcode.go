package quota

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/upstream"
)

const commandCodeBase = "https://api.commandcode.ai"

// readCommandCode reads the account's credits and its two windows. The identity
// headers are the CLI's, from the provider's data file.
func readCommandCode(ctx context.Context, r *Reader, e upstream.Entry, a Access) (Reading, error) {
	h := keyed(e, identity(e), a.Token)
	h["Accept"] = "application/json"
	code, raw, err := r.do(ctx, request{URL: commandCodeBase + "/alpha/whoami?limits=1", Headers: h})
	if err != nil {
		return failed("Command Code", err)
	}
	if code != http.StatusOK {
		return answered("Command Code", code)
	}
	org := gjson.GetBytes(raw, "org.id").String()
	code, credits, err := r.do(ctx, request{URL: commandCodeURL("/alpha/billing/credits", org), Headers: h})
	if err != nil {
		return failed("Command Code", err)
	}
	if code != http.StatusOK {
		return answered("Command Code", code)
	}
	out := Reading{Quotas: map[string]Quota{}, Plan: "Command Code"}
	var period time.Time
	if code, subs, err := r.do(ctx, request{URL: commandCodeURL("/alpha/billing/subscriptions", org), Headers: h}); err == nil && code == http.StatusOK {
		if name := gjson.GetBytes(subs, "data.planId").String(); name != "" {
			out.Plan = name
		}
		period = stampOf(gjson.GetBytes(subs, "data.currentPeriodEnd"))
	}
	out.Quotas["Credits"] = commandCodeCredits(credits, period)
	data := gjson.ParseBytes(credits)
	for _, w := range []struct{ path, label string }{
		{"windowLimits.fiveHour", "Session (5h)"},
		{"windowLimits.weekly", "Weekly"},
	} {
		v := data.Get(w.path)
		if !v.IsObject() {
			continue
		}
		q := window(v.Get("used").Float(), v.Get("cap").Float(), stampOf(v.Get("resetAt")))
		q.DisplayName, q.Recurring = w.label, true
		out.Quotas[w.label] = q
	}
	return out, nil
}

// commandCodeCredits is the credit balance the account holds. The plan's cap is
// not in the answer, so the row is a balance and not a window.
func commandCodeCredits(raw []byte, reset time.Time) Quota {
	data := gjson.ParseBytes(raw).Get("credits")
	left := data.Get("monthlyCredits").Float() + data.Get("purchasedCredits").Float() + data.Get("freeCredits").Float()
	q := balance(left, "credits", "")
	q.ResetAt = stamp(reset)
	q.DisplayName = "Credits"
	return q
}

// commandCodeURL adds the organisation to a billing address.
func commandCodeURL(path, org string) string {
	if org == "" {
		return commandCodeBase + path
	}
	return commandCodeBase + path + "?orgId=" + strings.TrimSpace(org)
}
