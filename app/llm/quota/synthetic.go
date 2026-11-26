package quota

import (
	"context"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/upstream"
)

const syntheticQuotasURL = "https://api.synthetic.new/v2/quotas"

// readSynthetic reads the two request buckets and the weekly credit pool. The
// pool publishes only the share still available, so it is shown as a bar out of
// 100 rather than as a count the API does not give.
func readSynthetic(ctx context.Context, r *Reader, e upstream.Entry, a Access) (Reading, error) {
	raw, stop, early, earlyErr := r.getOK(ctx, "Synthetic", request{URL: syntheticQuotasURL, Headers: keyedFor(e, a)})
	if stop {
		return early, earlyErr
	}
	return parseSynthetic(raw), nil
}

func parseSynthetic(raw []byte) Reading {
	out := Reading{Quotas: map[string]Quota{}, Plan: "Synthetic"}
	data := gjson.ParseBytes(raw)
	for _, b := range []struct{ name, path string }{{"Subscription", "subscription"}, {"Search (Hourly)", "search.hourly"}} {
		bucket := data.Get(b.path)
		total := bucket.Get("limit").Float()
		if total <= 0 {
			continue
		}
		used := min(total, max(0, bucket.Get("requests").Float()))
		out.Quotas[b.name] = window(used, total, stampOf(bucket.Get("renewsAt")))
	}
	if left := data.Get("planWeeklyLimit.percentRemaining"); left.Type == gjson.Number || (left.Type == gjson.String && left.String() != "") {
		out.Quotas["Weekly"] = share(100-clampPercent(left.Float()), stampOf(data.Get("planWeeklyLimit.nextRegenAt")))
	}
	if len(out.Quotas) == 0 {
		out.Message = "Synthetic reported no quota for this key"
	}
	return out
}
