package quota

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/upstream"
)

const (
	ollamaUsageURL   = "https://ollama.com/api/usage"
	ollamaMeURL      = "https://ollama.com/api/me"
	ollamaBalanceURL = "https://ollama.com/api/balance"
)

// readOllama reads the cloud plan's windows, then the account's balance when the
// plan reports none.
func readOllama(ctx context.Context, r *Reader, e upstream.Entry, a Access) (Reading, error) {
	h := bearerFor(e, a)
	raw, stop, early, earlyErr := r.getOK(ctx, "Ollama", request{URL: ollamaUsageURL, Headers: h})
	if stop {
		return early, earlyErr
	}
	out := parseOllamaPlan(raw)
	if len(out.Quotas) > 0 {
		out.Plan = ollamaPlan(ctx, r, h)
		return out, nil
	}
	raw, stop, early, earlyErr = r.getOK(ctx, "Ollama", request{URL: ollamaBalanceURL, Headers: h})
	if stop {
		return early, earlyErr
	}
	return parseOllamaBalance(raw), nil
}

// ollamaPlan reads the plan's name, which the usage answer does not carry.
func ollamaPlan(ctx context.Context, r *Reader, h map[string]string) string {
	code, raw, err := r.do(ctx, request{Method: http.MethodPost, URL: ollamaMeURL, Headers: h})
	if err != nil || code != http.StatusOK {
		return "Ollama Cloud"
	}
	plan := gjson.GetBytes(raw, "Plan").String()
	if plan == "" {
		return "Ollama Cloud"
	}
	return strings.ToUpper(plan[:1]) + strings.ToLower(plan[1:])
}

var ollamaWindows = []struct{ key, label string }{
	{"session", "Session (5h)"},
	{"weekly", "Weekly (7d)"},
	{"monthly", "Monthly"},
}

func parseOllamaPlan(raw []byte) Reading {
	out := Reading{Quotas: map[string]Quota{}}
	for _, w := range ollamaWindows {
		ratio := gjson.GetBytes(raw, "limits."+w.key+".usage")
		if !ratio.Exists() {
			continue
		}
		q := share(ratio.Float()*100, time.Time{})
		q.DisplayName = w.label
		out.Quotas[w.label] = q
	}
	if len(out.Quotas) == 0 {
		out.Message = "Ollama reported no quota"
	}
	return out
}

func parseOllamaBalance(raw []byte) Reading {
	out := Reading{Quotas: map[string]Quota{}, Plan: "Ollama Cloud"}
	data := gjson.ParseBytes(raw)
	for _, w := range []struct{ key, label string }{{"session", "Session (5h)"}, {"weekly", "Weekly (7d)"}} {
		inc := data.Get("included." + w.key)
		if !inc.Exists() {
			continue
		}
		left := inc.Get("remaining_percent")
		if !left.Exists() {
			// A window that names no share says nothing; it does not say full.
			continue
		}
		q := share(100-clampPercent(left.Float()), stampOf(inc.Get("resets_at")))
		q.DisplayName = w.label
		out.Quotas[w.label] = q
	}
	allowance, left := data.Get("included.allowance_usd"), data.Get("included.balance_usd")
	if allowance.Exists() && left.Exists() {
		total := allowance.Float()
		q := window(total-left.Float(), total, stampOf(data.Get("included.period.until")))
		q.DisplayName, q.Unit = "Allowance ($)", "USD"
		out.Quotas["Allowance ($)"] = q
	}
	if bought := data.Get("purchased.balance_usd").Float(); bought > 0 {
		q := balance(bought, "USD", "$")
		q.DisplayName = "Purchased Credits"
		out.Quotas["Purchased Credits"] = q
	}
	if len(out.Quotas) == 0 {
		out.Message = "Ollama reported no quota"
	}
	return out
}
