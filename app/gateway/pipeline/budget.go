package pipeline

import (
	"context"
	"net/http"

	"rllmx/app/access/quota"
	"rllmx/app/gateway/account"
	"rllmx/app/llm/routing"
	"rllmx/app/platform/failure"
)

// Budget decides whether a key may spend one more request.
type Budget interface {
	Check(ctx context.Context, keyID string) (quota.Refusal, error)
}

// budget answers a key whose budget is spent, or fails closed when the usage
// cannot be read. ok is false when the request must stop.
func (c *call) budget(ctx context.Context) (Output, bool) {
	if c.p.d.Budget == nil || c.in.Caller.ID == "" || c.in.Caller.Anonymous || c.budgetPassed {
		return Output{}, true
	}
	// A request whose context ended never reaches the provider: report the
	// client's cancel (or the call's own deadline) rather than a store fault.
	if ctx.Err() != nil {
		return c.stopped(ctx), false
	}
	ref, err := c.p.d.Budget.Check(ctx, c.in.Caller.ID)
	if err != nil {
		if ctx.Err() != nil {
			return c.stopped(ctx), false
		}
		if c.p.d.Log != nil {
			c.p.d.Log.Warningf("pipeline: a key's budget cannot be checked: %v", err)
		}
		return c.fail(http.StatusServiceUnavailable, "budget_unavailable", "The request budget cannot be checked"), false
	}
	if !ref.Refused {
		c.budgetPassed = true
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

// nameIntendedAccount names the account a refused request would have reached,
// so its row is not a row of dashes. The pick records nothing: no use happened.
// A combo has no account of its own, so it is left out.
func (c *call) nameIntendedAccount(ctx context.Context, t routing.Target) {
	if t.Combo != "" || c.p.d.Selector == nil {
		return
	}
	found, err := c.p.d.Selector.Peek(ctx, account.Request{
		Provider: t.Provider, Model: t.Model, RequestedModel: c.requested,
		Prefer: c.in.Connection, Hold: c.hold(t.Provider, t.Model),
	})
	if err != nil {
		return
	}
	c.row.ConnectionID = found.Conn.ID
}
