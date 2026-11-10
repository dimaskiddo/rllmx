package executor

import (
	"context"
	"strings"

	"rllmx/app/upstream"
)

// The Token Plan clusters. A key belongs to exactly one, so a wrong region is
// refused rather than sent.
var tokenPlanHosts = map[string]string{
	"sgp": "https://token-plan-sgp.xiaomimimo.com",
	"cn":  "https://token-plan-cn.xiaomimimo.com",
	"ams": "https://token-plan-ams.xiaomimimo.com",
}

const tokenPlanDefaultRegion = "sgp"

// XiaomiTokenPlan serves a Token Plan key: the cluster picks the host, and the
// client's format picks the path, so a Claude client reaches the native
// Anthropic lane with no translation.
type XiaomiTokenPlan struct {
	Default *Default
}

// Execute builds the address for the account's cluster and sends.
func (x *XiaomiTokenPlan) Execute(ctx context.Context, req Request) (Response, error) {
	if req.Key == "" {
		return Response{}, ErrNoCredential
	}
	req.URL = tokenPlanAddress(req.Account, tokenPlanFormat(req))
	return x.Default.Execute(ctx, req)
}

// tokenPlanFormat is the lane the request goes to: the model's own target format
// when its data names one, else the provider's.
func tokenPlanFormat(req Request) string {
	if req.Spec.TargetFormat != "" {
		return req.Spec.TargetFormat
	}
	return req.Entry.Transport.Format
}

// tokenPlanAddress is the cluster's chat or messages address. The entry's format
// is the one the pipeline chose, after a model's target format.
func tokenPlanAddress(account func(string) string, format string) string {
	region := tokenPlanDefaultRegion
	if account != nil {
		if r := strings.TrimSpace(account("region")); r != "" {
			region = r
		}
	}
	host, ok := tokenPlanHosts[region]
	if !ok {
		host = tokenPlanHosts[tokenPlanDefaultRegion]
	}
	if format == upstream.FormatClaude {
		return host + "/anthropic/v1/messages"
	}
	return host + "/v1/chat/completions"
}
