package quota

import (
	"context"
	"net/http"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/executor/cursor"
	"rllmx/app/llm/upstream"
)

const (
	cursorPeriodURL = "https://api2.cursor.sh/aiserver.v1.DashboardService/GetCurrentPeriodUsage"
	cursorAuthURL   = "https://api2.cursor.sh/auth/usage"
)

// readCursor reads the current billing period, then the per-model counts when
// the period answer carries no rows. The identity headers are the executor's own,
// so the account is asked the way it is charged.
func readCursor(ctx context.Context, r *Reader, e upstream.Entry, a Access) (Reading, error) {
	h := cursor.Headers(a.Token, stored(a.Conn, "machineId"))
	code, raw, err := r.do(ctx, request{Method: http.MethodPost, URL: cursorPeriodURL, Headers: h, Body: []byte(`{}`)})
	if err != nil {
		return failed("Cursor", err)
	}
	if code == http.StatusOK {
		if out := parseCursorPeriod(raw); len(out.Quotas) > 0 {
			return out, nil
		}
	}
	code, raw, err = r.do(ctx, request{URL: cursorAuthURL, Headers: bearerFor(e, a)})
	if err != nil {
		return failed("Cursor", err)
	}
	if code != http.StatusOK {
		return answered("Cursor", code)
	}
	out := parseCursorModels(raw)
	if out.Message == "" && gjson.ValidBytes(raw) {
		out.Plan = cursorPlan(ctx, r, h)
	}
	return out, nil
}

// cursorPlan names the subscription, which the usage answers leave out.
func cursorPlan(ctx context.Context, r *Reader, h map[string]string) string {
	code, raw, err := r.do(ctx, request{Method: http.MethodPost,
		URL:     "https://api2.cursor.sh/aiserver.v1.DashboardService/GetPlanInfo",
		Headers: h, Body: []byte(`{}`)})
	if err != nil || code != http.StatusOK {
		return "Cursor"
	}
	if name := gjson.GetBytes(raw, "planInfo.planName").String(); name != "" {
		return name
	}
	return "Cursor"
}

func parseCursorPeriod(raw []byte) Reading {
	out := Reading{Quotas: map[string]Quota{}, Plan: "Cursor"}
	data := gjson.ParseBytes(raw)
	reset := stampOf(data.Get("billingCycleEnd"))
	plan := data.Get("planUsage")
	if limit := plan.Get("limit").Float(); limit > 0 {
		spent := plan.Get("includedSpend").Float()
		if spent == 0 {
			spent = plan.Get("totalSpend").Float()
		}
		q := window(spent, limit, reset)
		q.DisplayName, q.Unit = "Plan (USD)", "USD"
		out.Quotas["Plan (USD)"] = q
	} else if used := plan.Get("totalPercentUsed"); used.Exists() {
		out.Quotas["Included usage (%)"] = named(share(used.Float(), reset), "Included usage (%)")
	}
	for _, w := range []struct{ path, label string }{
		{"planUsage.autoPercentUsed", "Auto models (%)"},
		{"planUsage.apiPercentUsed", "Named models (%)"},
	} {
		if v := data.Get(w.path); v.Exists() {
			out.Quotas[w.label] = named(share(v.Float(), reset), w.label)
		}
	}
	spend := data.Get("spendLimitUsage")
	for _, w := range []struct{ limit, used, label string }{
		{"individualLimit", "individualUsed", "On-demand (USD)"},
		{"pooledLimit", "pooledUsed", "Team on-demand (USD)"},
	} {
		total := spend.Get(w.limit).Float()
		if total <= 0 {
			continue
		}
		q := window(spend.Get(w.used).Float(), total, reset)
		q.DisplayName, q.Unit = w.label, "USD"
		out.Quotas[w.label] = q
	}
	return out
}

func parseCursorModels(raw []byte) Reading {
	out := Reading{Quotas: map[string]Quota{}, Plan: "Cursor"}
	gjson.ParseBytes(raw).ForEach(func(model, v gjson.Result) bool {
		if !v.IsObject() {
			return true
		}
		total := v.Get("maxRequestUsage").Float()
		if total <= 0 {
			return true
		}
		label := model.String() + " requests"
		out.Quotas[label] = named(window(v.Get("numRequests").Float(), total, stampOf(v.Get("startOfMonth"))), label)
		return true
	})
	if len(out.Quotas) == 0 {
		out.Message = "Cursor reported no quota"
	}
	return out
}

// named sets a row's label.
func named(q Quota, label string) Quota {
	q.DisplayName = label
	return q
}
