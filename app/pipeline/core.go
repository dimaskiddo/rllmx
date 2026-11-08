package pipeline

import (
	"context"
	"errors"
	"net/http"

	"rllmx/app/executor"
	"rllmx/app/repository"
)

func credential(conn repository.Connection) string {
	d := conn.Doc()
	if k, _ := d.GetString("apiKey"); k != "" {
		return k
	}
	k, _ := d.GetString("accessToken")
	return k
}

// try runs the request on one account. It returns either the final answer or
// the failure to report; the answer may itself be an error that is not the
// account's fault.
func (c *call) try(ctx context.Context, conn repository.Connection) (Output, *accountFail) {
	c.attemptStart = c.p.d.Now()
	if !c.upStream {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.p.d.BodyTimeout)
		defer cancel()
	}
	req := executor.Request{
		URL: c.url(), Body: c.body, Stream: c.upStream, Entry: c.dest.entry,
		Key: credential(conn), Auth: c.dest.auth,
	}
	if c.p.d.Overrides != nil {
		req.Overrides = c.p.d.Overrides.Headers(ctx, c.dest.provider)
	}
	resp, err := c.p.d.Executor.Execute(ctx, req)
	if err != nil {
		return c.executeFailed(ctx, err)
	}
	c.row.TTFBMs = c.p.d.Now().Sub(c.attemptStart).Milliseconds()
	if resp.Status >= http.StatusBadRequest {
		e := executor.ReadError(resp.Status, resp.Body)
		_ = resp.Body.Close()
		return Output{}, &accountFail{status: e.Status, text: e.Message, resetAt: e.ResetAt}
	}
	return c.respond(ctx, resp)
}

func (c *call) executeFailed(ctx context.Context, err error) (Output, *accountFail) {
	switch {
	case errors.Is(err, executor.ErrNoCredential):
		return Output{}, &accountFail{status: http.StatusUnauthorized, class: "auth", text: "no credentials"}
	case errors.Is(err, context.DeadlineExceeded) && ctx.Err() != nil:
		return Output{}, &accountFail{status: http.StatusGatewayTimeout, class: "timeout", text: "The upstream did not answer in time"}
	case ctx.Err() != nil:
		return c.closed(), nil
	}
	return Output{}, &accountFail{status: http.StatusBadGateway, class: "network", text: "The upstream could not be reached"}
}
