package pipeline

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/executor"
	"rllmx/app/oauth"
	"rllmx/app/repository"
	"rllmx/app/translate"
	"rllmx/app/upstream"
)

// accountField reads one text field of an account's provider specific data.
func accountField(conn repository.Connection) func(string) string {
	psd, _ := conn.Doc().GetRaw("providerSpecificData")
	email, _ := conn.Doc().GetString("email")
	return func(name string) string {
		if v := gjson.Get(psd, name).String(); v != "" {
			return v
		}
		switch name {
		case "email":
			return email
		case "connectionId":
			return conn.ID
		}
		return ""
	}
}

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
	conn, err := c.ready(ctx, conn)
	if err != nil {
		return Output{}, &accountFail{expired: true}
	}
	c.attemptStart = c.p.d.Now()
	if !c.upStream {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.p.d.BodyTimeout)
		defer cancel()
	}
	resp, err := c.send(ctx, conn)
	if err != nil {
		return c.executeFailed(ctx, err)
	}
	if c.retryable(conn, resp.Status) {
		first := executor.ReadError(resp.Status, resp.Body)
		_ = resp.Body.Close()
		fresh, rerr := c.p.d.Refresher.Renew(ctx, conn)
		switch {
		case errors.Is(rerr, oauth.ErrExpired):
			return Output{}, &accountFail{expired: true}
		case rerr != nil:
			c.logRefresh(rerr)
			return Output{}, &accountFail{status: first.Status, text: first.Message, resetAt: first.ResetAt}
		}
		if resp, err = c.send(ctx, fresh); err != nil {
			return c.executeFailed(ctx, err)
		}
	}
	c.row.TTFBMs = c.p.d.Now().Sub(c.attemptStart).Milliseconds()
	if resp.Status >= http.StatusBadRequest {
		e := executor.ReadError(resp.Status, resp.Body)
		_ = resp.Body.Close()
		return Output{}, &accountFail{status: e.Status, text: e.Message, resetAt: e.ResetAt}
	}
	return c.respond(ctx, resp)
}

// send makes one upstream call on a connection. The disguise is rebuilt for
// each call, so a retry on the next account never inherits another account's
// names.
func (c *call) send(ctx context.Context, conn repository.Connection) (executor.Response, error) {
	p := c.prepare(conn)
	c.tctx.ToolName = nil
	if p.cloaked {
		names := p.names
		c.tctx.ToolName = func(n string) string { return translate.Uncloak(n, names, true) }
	}
	c.cloakedNames = p.names
	req := executor.Request{
		URL: p.url, Body: p.body, Stream: c.upStream, Entry: c.dest.entry,
		Key: credential(conn), Auth: p.auth, Model: c.dest.model, Account: accountField(conn),
		Session: p.session, Compact: strings.HasSuffix(c.in.Path, "/compact"),
	}
	req.Spec, _ = c.p.d.Registry.Model(c.dest.provider, c.dest.model)
	switch c.dest.entry.Executor {
	case upstream.ExecutorCodex:
		req.Levels = c.p.d.Caps.Levels(c.dest.provider, c.dest.model, req.Spec.Levels)
	case upstream.ExecutorGrokCLI:
		req.Levels = req.Spec.Levels
	}
	if c.p.d.Overrides != nil {
		req.Overrides = c.p.d.Overrides.Headers(ctx, c.dest.provider)
	}
	return c.p.d.Executor.Execute(ctx, req)
}

func (c *call) executeFailed(ctx context.Context, err error) (Output, *accountFail) {
	switch {
	case errors.Is(err, executor.ErrBadAccount):
		return Output{}, &accountFail{status: http.StatusUnauthorized, class: "auth", text: "the account's data is missing or invalid"}
	case errors.Is(err, executor.ErrUnknownExecutor):
		return c.internal(ctx, err), nil
	case errors.Is(err, executor.ErrNoCredential):
		return Output{}, &accountFail{status: http.StatusUnauthorized, class: "auth", text: "no credentials"}
	case errors.Is(err, context.DeadlineExceeded) && ctx.Err() != nil:
		return Output{}, &accountFail{status: http.StatusGatewayTimeout, class: "timeout", text: "The upstream did not answer in time"}
	case ctx.Err() != nil:
		return c.closed(), nil
	}
	return Output{}, &accountFail{status: http.StatusBadGateway, class: "network", text: "The upstream could not be reached"}
}
