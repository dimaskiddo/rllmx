package quota

import (
	"context"
	"net/http"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/upstream"
	"rllmx/app/llm/zed"
)

// readZed reads the account's own record, which the service's client reads with
// the "user token" scheme rather than a bearer.
func readZed(ctx context.Context, r *Reader, e upstream.Entry, a Access) (Reading, error) {
	c := r.Zed
	if c == nil {
		c = &zed.Client{HTTP: r.Client}
	}
	code, raw, err := c.Profile(ctx, a.Conn)
	if err != nil {
		return failed("Zed", err)
	}
	if code != http.StatusOK {
		return answered("Zed", code)
	}
	return parseZed(raw), nil
}

func parseZed(raw []byte) Reading {
	out := Reading{Quotas: map[string]Quota{}, Plan: "Zed"}
	data := gjson.ParseBytes(raw)
	period := stampOf(data.Get("plan.subscription_period.ended_at"))
	if id := data.Get("plan.name").String(); id != "" {
		out.Plan = id
	}
	rows := []struct{ path, label string }{
		{"plan.usage.edit_predictions", "Edit Predictions"},
		{"plan.usage.model_requests", "Hosted Model Requests"},
	}
	for _, row := range rows {
		u := data.Get(row.path)
		if !u.Exists() {
			continue
		}
		total, unlimited := zedLimit(u.Get("limit"))
		if total <= 0 && !unlimited {
			continue
		}
		q := window(u.Get("used").Float(), total, period)
		q.Unlimited = q.Unlimited || unlimited
		q.DisplayName = row.label
		out.Quotas[row.label] = q
	}
	if len(out.Quotas) == 0 {
		out.Message = "Zed reported no quota"
	}
	return out
}

// zedLimit reads a limit the service gives as a number, as "unlimited", or as
// {"limited": n}.
func zedLimit(v gjson.Result) (float64, bool) {
	switch {
	case v.Type == gjson.String && v.String() == "unlimited":
		return 0, true
	case v.IsObject():
		return v.Get("limited").Float(), false
	}
	return v.Float(), false
}
