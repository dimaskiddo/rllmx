package quota

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/upstream"
)

// kiro hosts that answer a usage call, by how the account signed in.
const (
	kiroQHost    = "https://q.us-east-1.amazonaws.com"
	kiroCWHost   = "https://codewhisperer.us-east-1.amazonaws.com"
	kiroLimits   = "/getUsageLimits"
	kiroUsageQry = "isEmailRequired=true&origin=AI_EDITOR&resourceType=AGENTIC_REQUEST"
)

// readKiro reads what CodeWhisperer says is left of the agentic requests. The
// account's own region and profile decide the address; a key talks to the Q
// host first, as the executor does.
func readKiro(ctx context.Context, r *Reader, e upstream.Entry, a Access) (Reading, error) {
	hosts := []string{kiroCWHost, kiroQHost}
	if strings.EqualFold(stored(a.Conn, "authMethod"), "api_key") {
		hosts[0], hosts[1] = kiroQHost, kiroCWHost
	}
	if region := stored(a.Conn, "region"); region == "eu-central-1" {
		hosts[0] = strings.Replace(kiroQHost, "us-east-1", region, 1)
	}
	target := hosts[0] + kiroLimits + "?" + kiroUsageQry
	if profile := stored(a.Conn, "profileArn"); profile != "" {
		target += "&profileArn=" + url.QueryEscape(profile)
	}
	h := identity(e)
	bearer(h, a.Token)
	h["x-amz-user-agent"] = "aws-sdk-js/1.0.0 KiroIDE"
	h["User-Agent"] = "aws-sdk-js/1.0.0 KiroIDE"
	switch strings.ToLower(stored(a.Conn, "authMethod")) {
	case "api_key":
		h["TokenType"] = "API_KEY"
	case "external_idp":
		h["TokenType"] = "EXTERNAL_IDP"
	}
	code, raw, err := r.do(ctx, request{URL: target, Headers: h})
	if err != nil {
		return failed("Kiro", err)
	}
	if code != http.StatusOK {
		return answered("Kiro", code)
	}
	return parseKiro(raw), nil
}

func parseKiro(raw []byte) Reading {
	out := Reading{Quotas: map[string]Quota{}, Plan: "Kiro"}
	data := gjson.ParseBytes(raw)
	reset := stampOf(data.Get("nextDateReset"))
	if reset.IsZero() {
		reset = stampOf(data.Get("resetDate"))
	}
	if title := data.Get("subscriptionInfo.subscriptionTitle").String(); title != "" {
		out.Plan = title
	}
	for _, b := range data.Get("usageBreakdownList").Array() {
		kind := strings.ToLower(b.Get("resourceType").String())
		if kind == "" {
			kind = "unknown"
		}
		q := window(b.Get("currentUsageWithPrecision").Float(), b.Get("usageLimitWithPrecision").Float(), reset)
		q.DisplayName = kind
		out.Quotas[kind] = q
		if trial := b.Get("freeTrialInfo"); trial.IsObject() {
			at := stampOf(trial.Get("freeTrialExpiry"))
			if at.IsZero() {
				at = reset
			}
			t := window(trial.Get("currentUsageWithPrecision").Float(), trial.Get("usageLimitWithPrecision").Float(), at)
			t.DisplayName = kind + " free trial"
			out.Quotas[kind+"_freetrial"] = t
		}
	}
	if len(out.Quotas) == 0 {
		out.Message = "Kiro reported no quota"
	}
	return out
}
