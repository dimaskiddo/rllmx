package quota

import (
	"context"
	"net/http"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/upstream"
)

const freebuffSessionURL = "https://www.codebuff.com/api/v1/freebuff/session"

// readFreebuff reads the free session's per-model limits. recentCount is
// fractional and includes the running session's own reservation.
func readFreebuff(ctx context.Context, r *Reader, e upstream.Entry, a Access) (Reading, error) {
	h := bearer(identity(e), a.Token)
	h["User-Agent"] = "codebuff-cli/0.0.138"
	code, raw, err := r.do(ctx, request{URL: freebuffSessionURL, Headers: h})
	if err != nil {
		return failed("Freebuff", err)
	}
	switch code {
	case http.StatusNotFound:
		return Reading{Quotas: map[string]Quota{}, Plan: "Freebuff", Message: "Freebuff reported no session quota"}, nil
	case http.StatusOK:
	default:
		return answered("Freebuff", code)
	}
	out := Reading{Quotas: map[string]Quota{}, Plan: "Freebuff"}
	data := gjson.ParseBytes(raw)
	if data.Get("status").String() == "active" && data.Get("rateLimit").IsObject() {
		name := data.Get("model").String()
		if name == "" {
			name = "session"
		}
		out.Quotas[name] = freebuffRow(data.Get("rateLimit"), name)
	}
	data.Get("rateLimitsByModel").ForEach(func(model, rl gjson.Result) bool {
		out.Quotas[model.String()] = freebuffRow(rl, model.String())
		return true
	})
	if len(out.Quotas) == 0 {
		out.Message = "Freebuff reported no quota"
	}
	return out, nil
}

func freebuffRow(rl gjson.Result, name string) Quota {
	q := window(rl.Get("recentCount").Float(), rl.Get("limit").Float(), stampOf(rl.Get("resetAt")))
	q.DisplayName, q.Recurring = name, true
	return q
}
