package quota

import (
	"context"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/upstream"
)

const kimiUsageURL = "https://api.kimi.com/coding/v1/usages"

// readKimi reads a Kimi coding plan. A pasted key travels raw in x-api-key; a
// login's token goes as a bearer and names the device it was made for.
func readKimi(ctx context.Context, r *Reader, e upstream.Entry, a Access) (Reading, error) {
	h := identity(e)
	if a.Conn.AuthType == "oauth" || a.Conn.AuthType == "access_token" {
		bearer(h, a.Token)
		if e.OAuth != nil && e.OAuth.Account != nil {
			if id := e.OAuth.Account.Value(a.Conn.Data); id != "" {
				h[e.OAuth.Account.Name] = id
			}
		}
	} else {
		h = keyed(e, h, a.Token)
	}
	raw, stop, early, earlyErr := r.getOK(ctx, "Kimi", request{URL: kimiUsageURL, Headers: h})
	if stop {
		return early, earlyErr
	}
	return parseKimi(raw), nil
}

func parseKimi(raw []byte) Reading {
	out := Reading{Quotas: map[string]Quota{}, Plan: "Kimi Coding"}
	data := gjson.ParseBytes(raw)
	for _, u := range []gjson.Result{data.Get("usage"), data.Get("Usage")} {
		total := u.Get("limit").Float()
		if total <= 0 {
			total = u.Get("Limit").Float()
		}
		if total <= 0 {
			continue
		}
		used := u.Get("used").Float()
		if used == 0 {
			used = u.Get("Used").Float()
		}
		reset := u.Get("resetTime")
		if !reset.Exists() {
			reset = u.Get("ResetTime")
		}
		q := window(used, total, stampOf(reset))
		q.DisplayName = "Weekly"
		out.Quotas["Weekly"] = q
		break
	}
	// A plan reports several named rate limits; each keeps its own row, and the
	// tightest of them names a row by its own index so none overwrites another.
	for i, item := range data.Get("limits").Array() {
		d := item.Get("detail")
		total := d.Get("limit").Float()
		if total <= 0 {
			total = d.Get("Limit").Float()
		}
		if total <= 0 {
			continue
		}
		remaining := d.Get("remaining").Float()
		if remaining == 0 {
			remaining = d.Get("Remaining").Float()
		}
		reset := d.Get("resetTime")
		if !reset.Exists() {
			reset = d.Get("reset_at")
		}
		if !reset.Exists() {
			reset = d.Get("resetAt")
		}
		key := "Limit " + numberText(float64(i+1))
		name := item.Get("name").String()
		if name == "" {
			name = item.Get("displayName").String()
		}
		if name != "" {
			key = name
		}
		q := window(total-remaining, total, stampOf(reset))
		q.DisplayName = name
		q.Recurring = true
		out.Quotas[key] = q
	}
	if plan := data.Get("user.membership.level").String(); plan != "" {
		out.Plan = kimiPlan(plan)
	}
	if len(out.Quotas) == 0 {
		out.Message = "Kimi reported no quota"
	}
	return out
}

// kimiPlan turns a membership level into the name the service sells it under.
func kimiPlan(level string) string {
	switch level {
	case "LEVEL_BASIC":
		return "Kimi Moderato"
	case "LEVEL_STANDARD":
		return "Kimi Allegretto"
	case "LEVEL_PRO":
		return "Kimi Allegro"
	}
	return "Kimi Coding"
}
