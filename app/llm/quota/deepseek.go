package quota

import (
	"context"
	"net/http"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/upstream"
)

const deepseekBalanceURL = "https://api.deepseek.com/user/balance"

// readDeepSeek reads the account's balance. The service has no key-only window,
// so a connection with another auth type is not served.
func readDeepSeek(ctx context.Context, r *Reader, e upstream.Entry, a Access) (Reading, error) {
	h := keyed(e, identity(e), a.Token)
	h["Content-Type"] = "application/json"
	code, raw, err := r.do(ctx, request{URL: deepseekBalanceURL, Headers: h})
	if err != nil {
		return failed("DeepSeek", err)
	}
	if code != http.StatusOK {
		return answered("DeepSeek", code)
	}
	return parseDeepSeek(raw), nil
}

func parseDeepSeek(raw []byte) Reading {
	out := Reading{Quotas: map[string]Quota{}, Plan: "DeepSeek"}
	data := gjson.ParseBytes(raw)
	if data.Get("is_available").Exists() && !data.Get("is_available").Bool() {
		out.Plan = "DeepSeek (Insufficient Balance)"
	}
	for _, b := range data.Get("balance_infos").Array() {
		currency := b.Get("currency").String()
		if currency == "" {
			continue
		}
		q := balance(b.Get("total_balance").Float(), "USD", currency)
		q.DisplayName = "Balance (" + currency + ")"
		q.Unlimited = false
		out.Quotas["Balance ("+currency+")"] = q
	}
	if len(out.Quotas) == 0 {
		out.Message = "DeepSeek reported no balance"
	}
	return out
}
