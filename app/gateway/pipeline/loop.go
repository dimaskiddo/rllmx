package pipeline

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"rllmx/app/gateway/account"
	"rllmx/app/llm/catalog"
	"rllmx/app/llm/oauth"
	"rllmx/app/platform/failure"
)

// serve walks the provider's accounts until one answers, the failure belongs to
// the request, or none is left.
func (c *call) serve(ctx context.Context) Output {
	exclude := map[string]bool{}
	var last *accountFail
	sawExpired := false
	for {
		sel, err := c.p.d.Selector.Select(ctx, account.Request{
			Provider: c.dest.provider, Model: c.dest.model, RequestedModel: c.requested, Exclude: exclude,
			Hold: c.hold(),
		})
		if err != nil {
			return c.noAccount(ctx, err, last, sawExpired)
		}
		c.attempt++
		c.row.Attempt = c.attempt
		c.row.ConnectionID = sel.Conn.ID

		// A model the account's own provider does not list is the client's
		// mistake, not the account's: it ends the request here, before a send,
		// so no lock is written and no other account is tried for it.
		if c.p.d.Catalog != nil && c.p.d.Catalog.Known(ctx, c.dest.provider, sel.Conn, c.dest.model) == catalog.Unknown {
			return c.fail(http.StatusBadRequest, "invalid_request", "Model not found")
		}
		out, f := c.try(ctx, sel.Conn)
		if f == nil {
			return out
		}
		if f.expired {
			exclude[sel.Conn.ID] = true
			sawExpired = true
			continue
		}
		if f.gated {
			exclude[sel.Conn.ID] = true
			last = f
			continue
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

func (c *call) noAccount(ctx context.Context, err error, last *accountFail, sawExpired bool) Output {
	if ctx.Err() != nil {
		return c.closed()
	}
	var locked *account.AllLockedError
	switch {
	case last == nil && (sawExpired || errors.Is(err, account.ErrAllExpired)) && !errors.As(err, &locked):
		return c.expired()
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
		class = failure.ClassOf(f.status)
	}
	return c.fail(failure.ClientStatus(f.status), class, f.text)
}

// expired answers a request that found only accounts to be reconnected. It is
// a 503 like the all-locked answer, with no retry hint because waiting does not
// help: a 401 would make the client discard a good router key.
func (c *call) expired() Output {
	return c.fail(http.StatusServiceUnavailable, "expired", "["+c.dest.provider+"/"+c.dest.model+"] "+oauth.ReconnectMessage)
}
