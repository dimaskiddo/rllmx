package quota

import (
	"context"
	"net/http"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/upstream"
)

const vercelCreditsURL = "https://ai-gateway.vercel.sh/v1/credits"

// readVercel reads the gateway's spend and the credit left. The service reports
// no monthly allowance, so the remaining share is taken against what the account
// has held in all: the balance plus what it has spent.
func readVercel(ctx context.Context, r *Reader, e upstream.Entry, a Access) (Reading, error) {
	code, raw, err := r.do(ctx, request{URL: vercelCreditsURL, Headers: keyed(e, identity(e), a.Token)})
	if err != nil {
		return failed("Vercel AI Gateway", err)
	}
	if code != http.StatusOK {
		return answered("Vercel AI Gateway", code)
	}
	return parseVercel(raw), nil
}

func parseVercel(raw []byte) Reading {
	out := Reading{Quotas: map[string]Quota{}, Plan: "Pay-as-you-go"}
	data := gjson.ParseBytes(raw)
	left, spent := data.Get("balance").Float(), data.Get("total_used").Float()
	if left <= 0 && spent <= 0 {
		out.Message = "Vercel AI Gateway reported no credit allocation"
		return out
	}
	used := Quota{Total: spent, RemainingPercentage: 100, Unlimited: true, DisplayName: "Used (USD)", Unit: "USD"}
	used.Used = spent
	out.Quotas["Used (USD)"] = used
	q := window(spent, left+spent, stampOf(data.Get("period.until")))
	q.DisplayName, q.Unit = "Remaining (USD)", "USD"
	out.Quotas["Remaining (USD)"] = q
	return out
}
