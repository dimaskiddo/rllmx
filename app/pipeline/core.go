package pipeline

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

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
		case "projectId":
			p, _ := conn.Doc().GetString("projectId")
			return p
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

// antigravityID is the provider the quota gate serves.
const antigravityID = "antigravity"

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
	key, fail := c.keyFor(ctx, conn)
	if fail != nil {
		return Output{}, fail
	}
	conn, fail = c.withProject(ctx, conn, key)
	if fail != nil {
		return Output{}, fail
	}
	resp, err := c.send(ctx, conn, key)
	if err != nil {
		return c.executeFailed(ctx, err)
	}
	if c.retryable(conn, resp.Status) {
		first := executor.ReadError(resp.Status, resp.Body)
		_ = resp.Body.Close()
		fresh, newKey, rerr := c.renew(ctx, conn, key)
		switch {
		case errors.Is(rerr, oauth.ErrExpired):
			return Output{}, &accountFail{expired: true}
		case rerr != nil:
			c.logRefresh(rerr)
			return Output{}, &accountFail{status: first.Status, text: first.Message, resetAt: first.ResetAt}
		}
		if resp, err = c.send(ctx, fresh, newKey); err != nil {
			return c.executeFailed(ctx, err)
		}
	}
	c.row.TTFBMs = c.p.d.Now().Sub(c.attemptStart).Milliseconds()
	if resp.Status >= http.StatusBadRequest {
		e := executor.ReadError(resp.Status, resp.Body)
		_ = resp.Body.Close()
		fail := &accountFail{status: e.Status, text: e.Message, resetAt: e.ResetAt}
		c.gate(ctx, conn, key, fail)
		return Output{}, fail
	}
	return c.respond(ctx, resp)
}

// hold is the selector's view of the quota gate: an account it knows is spent
// for the model is skipped until the gate's time. Only Antigravity has a gate.
func (c *call) hold() func(string) (time.Time, bool) {
	if c.p.d.Quota == nil || c.dest.provider != antigravityID {
		return nil
	}
	return func(id string) (time.Time, bool) { return c.p.d.Quota.Skip(id, c.dest.model) }
}

// gate asks the quota gate about an Antigravity refusal that means "out of
// quota" (409 or 429). When the gate has an exact time for the account and model,
// the failure is marked gated, so the account is passed over until then without
// a stored lock; otherwise the common rules, with the provider's own hint, apply.
func (c *call) gate(ctx context.Context, conn repository.Connection, access string, fail *accountFail) {
	if c.p.d.Quota == nil || c.dest.provider != antigravityID {
		return
	}
	if fail.status != http.StatusTooManyRequests && fail.status != http.StatusConflict {
		return
	}
	if until, ok := c.p.d.Quota.Refused(ctx, conn.ID, access, c.dest.model); ok {
		fail.gated, fail.resetAt = true, until
	}
}

// withProject gives a Cloud Code account its project before a request goes out.
// The project is stored with the account the first time it is found, so this is
// a lookup only for an account that has none. An account whose project cannot be
// found fails here, with nothing sent.
func (c *call) withProject(ctx context.Context, conn repository.Connection, access string) (repository.Connection, *accountFail) {
	if e := c.dest.entry.Executor; e != upstream.ExecutorGeminiCLI && e != upstream.ExecutorAntigravity {
		return conn, nil
	}
	if p, _ := conn.Doc().GetString("projectId"); p != "" {
		return conn, nil
	}
	if c.p.d.Projects == nil {
		return conn, &accountFail{status: http.StatusUnauthorized, class: "auth", text: "the account has no project"}
	}
	project, err := c.p.d.Projects.Project(ctx, conn, access)
	switch {
	case ctx.Err() != nil:
		return conn, &accountFail{status: http.StatusGatewayTimeout, class: "timeout", text: "The upstream did not answer in time"}
	case oauth.ClassOf(err) == oauth.ClassTransport:
		return conn, &accountFail{status: http.StatusBadGateway, class: "network", text: "The upstream could not be reached"}
	case err != nil:
		return conn, &accountFail{status: http.StatusUnauthorized, class: "auth", text: "the account has no project"}
	}
	conn.Data = withTopLevel(conn.Data, "projectId", project)
	return conn, nil
}

// withTopLevel returns a connection's data text with one string key set.
func withTopLevel(data, key, value string) string {
	out, err := sjson.Set(data, key, value)
	if err != nil {
		return data
	}
	return out
}

// send makes one upstream call on a connection. The disguise is rebuilt for
// each call, so a retry on the next account never inherits another account's
// names.
func (c *call) send(ctx context.Context, conn repository.Connection, key string) (executor.Response, error) {
	p := c.prepare(conn)
	c.tctx.ToolName = nil
	if p.cloaked {
		names := p.names
		c.tctx.ToolName = func(n string) string { return translate.Uncloak(n, names, true) }
	}
	c.cloakedNames = p.names
	req := executor.Request{
		URL: p.url, Body: p.body, Stream: c.upStream, Entry: c.dest.entry,
		Key: key, Auth: p.auth, Model: c.dest.model, Account: accountField(conn),
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
