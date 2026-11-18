package quota

import (
	"context"
	"net/http"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/upstream"
)

const (
	grokBillingURL = "https://cli-chat-proxy.grok.com/v1/billing?format=credits"
	grokUserURL    = "https://cli-chat-proxy.grok.com/v1/user?include=subscription"
)

// readGrokCLI reads the CLI's billing period and the weekly credit share. The
// account's own headers are sent, since the service is the CLI's.
func readGrokCLI(ctx context.Context, r *Reader, e upstream.Entry, a Access) (Reading, error) {
	h := bearerFor(e, a)
	h["User-Agent"] = "grok-shell/1.0.44 (linux; x86_64)"
	h["x-xai-token-auth"] = "xai-grok-cli"
	h["x-grok-client-identifier"] = "grok-shell"
	h["x-grok-client-version"] = "1.0.44"
	h["x-grok-client-mode"] = "headless"
	for _, f := range []struct{ header, field string }{{"x-email", "email"}, {"x-userid", "userId"}} {
		if v := stored(a.Conn, f.field); v != "" {
			h[f.header] = v
		}
	}
	code, billing, err := r.do(ctx, request{URL: grokBillingURL, Headers: h})
	if err != nil {
		return failed("Grok", err)
	}
	if code != http.StatusOK {
		return answered("Grok", code)
	}
	out := parseGrokBilling(billing)
	// The user record names the plan; a failure there says nothing about the
	// limits, so it is not reported.
	if code, user, err := r.do(ctx, request{URL: grokUserURL, Headers: h}); err == nil && code == http.StatusOK {
		if name := grokPlan(user); name != "" {
			out.Plan = name
		}
	}
	if len(out.Quotas) == 0 {
		out.Message = "Grok reported no quota"
	}
	return out, nil
}

// parseGrokBilling reads the billing answer, which wraps its fields in `val`.
func parseGrokBilling(raw []byte) Reading {
	out := Reading{Quotas: map[string]Quota{}, Plan: "Grok"}
	data := gjson.ParseBytes(raw)
	if v := data.Get("val"); v.IsObject() {
		data = v
	}
	reset := stampOf(data.Get("billingPeriodEnd"))
	if reset.IsZero() {
		reset = stampOf(data.Get("currentPeriod.end"))
	}
	if limit := field(data, "monthly_limit"); limit > 0 {
		used := field(data, "included_used")
		if used == 0 {
			used = field(data, "total_used")
		}
		out.Quotas["Monthly included"] = named(window(used, limit, reset), "Monthly included")
	}
	if cap := field(data, "on_demand_cap"); cap > 0 {
		out.Quotas["On-demand"] = named(window(field(data, "on_demand_used"), cap, reset), "On-demand")
	}
	if left := field(data, "prepaid_balance"); left > 0 {
		q := balance(left, "USD", "$")
		out.Quotas["Prepaid"] = named(q, "Prepaid")
	}
	if pct := gjson.Get(data.Raw, "creditUsagePercent"); pct.Exists() {
		out.Quotas["Weekly SuperGrok"] = named(share(pct.Float(), reset), "Weekly SuperGrok")
	}
	return out
}

// grokPlan reads the subscription's name from the user record.
func grokPlan(raw []byte) string {
	data := gjson.ParseBytes(raw)
	if v := data.Get("val"); v.IsObject() {
		data = v
	}
	for _, path := range []string{"subscription.name", "subscription.plan", "subscription.tier"} {
		if name := data.Get(path).String(); name != "" {
			return name
		}
	}
	return ""
}
