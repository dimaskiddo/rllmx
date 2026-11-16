package pipeline

import (
	"context"
	"net/http"

	"rllmx/app/access/quota"
	"rllmx/app/platform/failure"
)

// Budget decides whether a key may spend one more request.
type Budget interface {
	Check(ctx context.Context, keyID string) (quota.Refusal, error)
}

// budget answers a key whose budget is spent, or fails closed when the usage
// cannot be read. ok is false when the request must stop.
func (c *call) budget(ctx context.Context) (Output, bool) {
	if c.p.d.Budget == nil || c.in.Caller.ID == "" || c.in.Caller.Anonymous {
		return Output{}, true
	}
	ref, err := c.p.d.Budget.Check(ctx, c.in.Caller.ID)
	if err != nil {
		if c.p.d.Log != nil {
			c.p.d.Log.Warningf("pipeline: a key's budget cannot be checked: %v", err)
		}
		return c.fail(http.StatusServiceUnavailable, "unknown", "The request budget cannot be checked"), false
	}
	if !ref.Refused {
		return Output{}, true
	}
	return c.refuseBudget(ref), false
}

// refuseBudget answers a spent budget with what is left and when it resets.
func (c *call) refuseBudget(ref quota.Refusal) Output {
	c.errClass = "key_quota"
	h := failure.Headers()
	for k, v := range failure.QuotaHeaders(ref.Remaining.Requests, ref.Remaining.Tokens, ref.Remaining.Cost, ref.RetryAfter) {
		h[k] = v
	}
	return Output{Status: http.StatusTooManyRequests, Header: h, Body: failure.Quota(c.failureFormat(), "The API key's budget is spent")}
}
