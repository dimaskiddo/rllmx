package quota

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/upstream"
)

// kiro hosts that answer a usage call.
const (
	kiroQHost    = "https://q.us-east-1.amazonaws.com"
	kiroCWHost   = "https://codewhisperer.us-east-1.amazonaws.com"
	kiroLimits   = "/getUsageLimits"
	kiroUsageQry = "isEmailRequired=true&origin=AI_EDITOR&resourceType=AGENTIC_REQUEST"
	kiroTarget   = "AmazonCodeWhispererService.GetUsageLimits"
)

// readKiro reads what CodeWhisperer says is left of the agentic requests. The
// service answers on more than one address and in more than one shape, so it
// tries each in turn and takes the first that answers. A refusal on all of them
// is a message, not an error: the chat path reaches a different host, so the
// login may still be good, and renewing it would not help.
func readKiro(ctx context.Context, r *Reader, e upstream.Entry, a Access) (Reading, error) {
	method := strings.ToLower(stored(a.Conn, "authMethod"))
	h := kiroHeaders(e, a, method)
	profile := stored(a.Conn, "profileArn")
	// Only the Q host is served outside us-east-1.
	cw, q := kiroCWHost, kiroQHost
	if region := stored(a.Conn, "region"); region == "eu-central-1" {
		q = strings.Replace(kiroQHost, "us-east-1", region, 1)
	}
	post := map[string]string{"origin": "AI_EDITOR", "resourceType": "AGENTIC_REQUEST"}
	if profile != "" {
		post["profileArn"] = profile
	}
	body, err := json.Marshal(post)
	if err != nil {
		return failed("Kiro", err)
	}
	getURL := kiroUsageQry
	if profile != "" {
		getURL += "&profileArn=" + url.QueryEscape(profile)
	}
	postHeaders := maps.Clone(h)
	postHeaders["Content-Type"] = "application/x-amz-json-1.0"
	postHeaders["X-Amz-Target"] = kiroTarget

	attempts := []request{
		{URL: cw + kiroLimits + "?" + getURL, Headers: h},
		{Method: http.MethodPost, URL: cw, Headers: postHeaders, Body: body},
		{URL: q + kiroLimits + "?" + getURL, Headers: h},
	}
	sawAuth, last := false, 0
	for _, req := range attempts {
		code, raw, err := r.do(ctx, req)
		if err != nil {
			if ctx.Err() != nil {
				return failed("Kiro", err)
			}
			continue
		}
		if code == http.StatusOK {
			return parseKiro(raw), nil
		}
		last = code
		if code == http.StatusUnauthorized || code == http.StatusForbidden {
			sawAuth = true
		}
	}
	if sawAuth {
		return Reading{Message: "Kiro quota API rejected the current token. Chat may still work."}, nil
	}
	if last == 0 {
		return Reading{Message: "Kiro could not be reached"}, nil
	}
	return Reading{Message: fmt.Sprintf("Kiro answered %d", last)}, nil
}

// kiroHeaders is the identity a usage call carries: the token the way the
// account signed in, and the IDE's own identity.
func kiroHeaders(e upstream.Entry, a Access, method string) map[string]string {
	h := bearerFor(e, a)
	h["Accept"] = "application/json"
	seed := upstream.KiroSeed{
		ClientID: stored(a.Conn, "clientId"), RefreshToken: stored(a.Conn, "refreshToken"),
		ProfileARN: stored(a.Conn, "profileArn"), AccessToken: a.Token,
	}
	if method == "api_key" {
		seed.APIKey, seed.AccessToken = a.Token, ""
	}
	maps.Copy(h, upstream.KiroIdentity(seed, upstream.KiroRuntime))
	switch method {
	case "api_key":
		h["TokenType"] = "API_KEY"
	case "external_idp":
		h["TokenType"] = "EXTERNAL_IDP"
	}
	return h
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
