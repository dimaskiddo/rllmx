package controllers

import (
	"context"
	nethttp "net/http"
	"time"

	contractshttp "github.com/goravel/framework/contracts/http"

	"rllmx/app/access/quota"
	apphttp "rllmx/app/http"
	"rllmx/app/platform/failure"
)

// ClientUsageController answers the usage questions a client tool asks its
// provider, from the calling key's own budget. Claude Code reads how much of its
// session and week it has used, and OpenCode its rolling and monthly windows; a
// key with a budget gets a true answer instead of the provider's, which is not
// the budget this key lives under. It makes no upstream call.
type ClientUsageController struct {
	// Standing reads one key's standing in each budget window; nil answers 503.
	Standing func(ctx context.Context, keyID string) (quota.Standings, error)
}

// Claude answers GET /api/oauth/usage and GET /v1/organizations/{org}/usage with
// the day as the five-hour session and the month as the week.
func (c *ClientUsageController) Claude(ctx contractshttp.Context) contractshttp.Response {
	s, ok := c.read(ctx)
	if !ok {
		return nil
	}
	window := func(w quota.Standing) contractshttp.Json {
		return contractshttp.Json{"utilization": w.Percent, "resets_at": stamp(w.ResetsAt)}
	}
	return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{
		"five_hour": window(s.Day),
		"seven_day": window(s.Month),
	})
}

// OpenCode answers GET /v1/usage with the day as the rolling window and the month
// as the monthly one. A window no budget limits is still answered at zero: the
// reference reader drops one that is absent, and a client would read that as a
// failure to fetch.
func (c *ClientUsageController) OpenCode(ctx contractshttp.Context) contractshttp.Response {
	s, ok := c.read(ctx)
	if !ok {
		return nil
	}
	window := func(w quota.Standing) contractshttp.Json {
		return contractshttp.Json{"percent": w.Percent, "resetsAt": stamp(w.ResetsAt)}
	}
	return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{
		"usage": contractshttp.Json{"rolling": window(s.Day), "monthly": window(s.Month)},
	})
}

// read answers for the caller the guard recorded, and writes the refusal itself
// when there is none or the budget cannot be read. ok is false when it did.
func (c *ClientUsageController) read(ctx contractshttp.Context) (quota.Standings, bool) {
	r := ctx.Request().Origin()
	w := ctx.Response().Writer()
	format := failure.ForPath(r.URL.Path)
	caller, ok := apphttp.CallerFrom(ctx)
	if !ok {
		failure.Write(w, format, nethttp.StatusUnauthorized, "API key required")
		return quota.Standings{}, false
	}
	if c.Standing == nil {
		failure.Write(w, format, nethttp.StatusServiceUnavailable, "")
		return quota.Standings{}, false
	}
	id := caller.ID
	if caller.Anonymous {
		id = ""
	}
	s, err := c.Standing(r.Context(), id)
	if err != nil {
		failure.Write(w, format, nethttp.StatusServiceUnavailable, "")
		return quota.Standings{}, false
	}
	h := ctx.Response().Header
	h("Cache-Control", "no-store")
	tight := s.Day
	if s.Month.Percent > tight.Percent {
		tight = s.Month
	}
	for name, vals := range failure.RateLimitHeaders(tight.Percent >= 100, tight.ResetsAt) {
		h(name, vals[0])
	}
	return s, true
}

// stamp is a reset moment in the form the provider sends; empty for all time.
func stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
