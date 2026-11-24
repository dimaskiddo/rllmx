package quota

import (
	"context"
	"math"
	"net/http"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/upstream"
)

const (
	deeprelayBalanceURL = "https://api.deeprelay.ai/v1/billing/balance"
	deeprelaySpendURL   = "https://api.deeprelay.ai/v1/usage?bucket=day"
	// deeprelaySpendWait keeps the optional spend history from delaying the
	// balance a reader came for.
	deeprelaySpendWait = 5 * time.Second
)

// readDeepRelay reads the prepaid wallet, the hourly burn rate when the account
// reports one, and the recent spend. DeepRelay bills per token with no cap, so
// the rows inform and nothing drains toward a limit.
func readDeepRelay(ctx context.Context, r *Reader, e upstream.Entry, a Access) (Reading, error) {
	h := keyedFor(e, a)
	code, raw, err := r.do(ctx, request{URL: deeprelayBalanceURL, Headers: h})
	if err != nil {
		return failed("DeepRelay", err)
	}
	switch code {
	case http.StatusOK:
	case http.StatusForbidden:
		// A key without the billing scope still serves chat, so renewing it
		// would change nothing.
		msg := "DeepRelay refused this key"
		if c := gjson.GetBytes(raw, "error.code").String(); c == "insufficient_scope" {
			msg = "DeepRelay key is missing the billing:read scope needed to read the wallet"
		}
		return Reading{Plan: "DeepRelay", Message: msg}, nil
	default:
		return answered("DeepRelay", code)
	}
	out := parseDeepRelay(raw)
	if out.Message != "" {
		return out, nil
	}
	spendCtx, cancel := context.WithTimeout(ctx, deeprelaySpendWait)
	defer cancel()
	if code, raw, err := r.do(spendCtx, request{URL: deeprelaySpendURL, Headers: h}); err == nil && code == http.StatusOK {
		if q, ok := deeprelaySpend(raw); ok {
			out.Quotas["Spend (30d)"] = q
		}
	}
	return out, nil
}

func parseDeepRelay(raw []byte) Reading {
	out := Reading{Quotas: map[string]Quota{}, Plan: "DeepRelay"}
	data := gjson.ParseBytes(raw)
	if !data.IsObject() {
		out.Message = "DeepRelay balance answer was not readable"
		return out
	}
	dollars := data.Get("balance_cents").Float() / 100
	if d := data.Get("balance_dollars"); d.Exists() {
		dollars = d.Float()
	}
	dollars = max(0, math.Round(dollars*100)/100)
	wallet := balance(dollars, "USD", "USD")
	if dollars == 0 {
		wallet.RemainingPercentage = 0
	}
	out.Quotas["Balance ($)"] = wallet
	if burn := math.Round(data.Get("burn_cents_per_hour").Float()) / 100; burn > 0 {
		out.Quotas["Burn rate ($/h)"] = Quota{Used: burn, RemainingPercentage: 100, Unlimited: true, Unit: "USD", Currency: "USD"}
	}
	return out
}

// deeprelaySpend sums the day buckets into one row. An empty history is no row:
// "0 spent" would read as a fact about an account with no records.
func deeprelaySpend(raw []byte) (Quota, bool) {
	rows := gjson.GetBytes(raw, "data").Array()
	if len(rows) == 0 {
		return Quota{}, false
	}
	var cents float64
	for _, row := range rows {
		cents += row.Get("cost_cents").Float()
	}
	return Quota{Used: math.Round(cents) / 100, RemainingPercentage: 100, Unlimited: true, Unit: "USD", Currency: "USD"}, true
}
