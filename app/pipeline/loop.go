package pipeline

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"rllmx/app/account"
	"rllmx/app/failure"
)

// serve walks the provider's accounts until one answers, the failure belongs to
// the request, or none is left.
func (c *call) serve(ctx context.Context) Output {
	exclude := map[string]bool{}
	var last *accountFail
	for {
		sel, err := c.p.d.Selector.Select(ctx, account.Request{
			Provider: c.dest.provider, Model: c.dest.model, RequestedModel: c.requested, Exclude: exclude,
		})
		if err != nil {
			return c.noAccount(ctx, err, last)
		}
		c.attempt++
		c.row.Attempt = c.attempt
		c.row.ConnectionID = sel.Conn.ID

		out, f := c.try(ctx, sel.Conn)
		if f == nil {
			return out
		}
		res, err := c.p.d.Reporter.Fail(ctx, account.FailInput{
			ConnID: sel.Conn.ID, Provider: c.dest.provider, Model: c.dest.model,
			Status: f.status, Text: f.text, ResetAt: f.resetAt,
		})
		if err != nil {
			if c.p.d.Log != nil {
				c.p.d.Log.Warningf("pipeline: recording a failed account failed: %v", err)
			}
			return c.upstreamFail(f)
		}
		if !res.Fallback {
			return c.upstreamFail(f)
		}
		exclude[sel.Conn.ID] = true
		last = f
	}
}

func (c *call) noAccount(ctx context.Context, err error, last *accountFail) Output {
	if ctx.Err() != nil {
		return c.closed()
	}
	var locked *account.AllLockedError
	switch {
	case errors.As(err, &locked):
		msg := fmt.Sprintf("[%s/%s] ", c.dest.provider, c.dest.model)
		if locked.LastError != "" {
			msg += locked.LastError + " "
		}
		msg += "(" + locked.Human + ")"
		body, extra := failure.Unavailable(c.failureFormat(), msg, locked.RetryAt, c.p.d.Now(), nil)
		h := failure.Headers()
		for k, v := range extra {
			h[k] = v
		}
		c.errClass = "no_account"
		return Output{Status: http.StatusServiceUnavailable, Header: h, Body: body}
	case errors.Is(err, account.ErrNoConnections):
		if last != nil {
			return c.upstreamFail(last)
		}
		return c.fail(http.StatusNotFound, "no_account", "No active credentials for provider: "+c.dest.provider)
	}
	return c.internal(ctx, err)
}

// upstreamFail answers the client with the failure of the last account tried.
func (c *call) upstreamFail(f *accountFail) Output {
	class := f.class
	if class == "" {
		class = classOf(f.status)
	}
	return c.fail(clientStatus(f.status), class, f.text)
}
