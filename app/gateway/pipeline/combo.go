package pipeline

import (
	"context"
	"errors"
	"net/http"
	"time"

	"rllmx/app/gateway/combo"
	"rllmx/app/gateway/conversation"
	"rllmx/app/llm/routing"
	"rllmx/app/llm/translate"
	"rllmx/app/llm/upstream"
	apptext "rllmx/app/platform/text"
	"rllmx/app/store/document"
)

// settleCooldown is the longest reset a combo waits out before it moves on; a
// longer one ends the search and is returned.
const settleCooldown = 5 * time.Second

// comboRun tries a combo's members in order and answers with the first that
// serves content. `c` is the client's request: access and the budget were
// already checked on the combo's name, and it owns the one usage row.
func (c *call) comboRun(ctx context.Context, name string) Output {
	policy, order, out, ok := c.comboOrder(ctx, name)
	if !ok {
		return out
	}
	c.row.ComboName = name
	if policy.Strategy == combo.StrategyFusion {
		return c.fusion(ctx, policy, order)
	}
	return c.walkCombo(ctx, order)
}

// comboOrder resolves a combo's members and the order this request tries them
// in. ok is false when the request is already answered, in out.
func (c *call) comboOrder(ctx context.Context, name string) (policy combo.Policy, order []routing.Member, out Output, ok bool) {
	// A nested combo arrives from the type assertion below; the resolver is the
	// one place that knows the members.
	members, err := c.expand(ctx, name)
	if err != nil {
		return policy, nil, c.comboRefused(ctx, err), false
	}
	policy, err = c.comboPolicy(ctx, name)
	if err != nil {
		return policy, nil, c.internal(ctx, err), false
	}
	var smart *combo.Smart
	if policy.Strategy == combo.StrategySmart {
		smart = &combo.Smart{
			Router:  smartRouter{call: c},
			Sticky:  c.p.d.Sticky,
			Session: c.comboSession(),
			State:   c.comboState(),
			Now:     c.p.d.Now,
		}
	}
	// Fusion asks every member, so the breaker does not reorder its panel; a
	// search moves an open provider to the end instead.
	var open func(string) bool
	if policy.Strategy != combo.StrategyFusion {
		open = c.providerOpen
	}
	order, found := combo.Order(ctx, policy, members, c.comboCaps, open, translate.DetectNeeds(c.req), c.p.d.Rotation, smart)
	if !found {
		return policy, nil, c.fail(http.StatusBadRequest, "invalid_request", "The combo names no model"), false
	}
	return policy, order, Output{}, true
}

// walkCombo tries the members in order and answers with the first that serves.
// When none does, the answer is the first refusal that blamed the request, else
// the last failure.
func (c *call) walkCombo(ctx context.Context, order []routing.Member) Output {
	var firstRequestScoped, last *comboFailure
	for i, m := range order {
		out, fail := c.comboMember(ctx, m, nil)
		if fail == nil {
			// Answered. The row names the member that served it.
			c.row.Provider, c.row.Model = m.Provider, m.Model
			return out
		}
		if ctx.Err() != nil {
			return c.closed()
		}
		if out.Status == 0 {
			out = c.upstreamFail(fail)
		}
		last = &comboFailure{out: out, class: c.errClass}
		if fail.expired || fail.gated {
			continue
		}
		if fail.empty && i < len(order)-1 {
			// No content on a member that is not the last: move on. The last
			// member's empty answer is returned, below, as it stands.
			continue
		}
		switch classifyMember(fail) {
		case memberRequestScoped:
			if firstRequestScoped == nil {
				firstRequestScoped = last
			}
		case memberRetry:
			if end, stop := c.settleRetry(ctx, fail, out, i == len(order)-1); stop {
				return end
			}
		}
	}
	if firstRequestScoped != nil {
		return c.settle(firstRequestScoped)
	}
	if last != nil {
		return c.settle(last)
	}
	return c.fail(http.StatusServiceUnavailable, "no_account", "No combo model is available")
}

// settleRetry decides what a 502, 503 or 504 does to the walk. A short cooldown
// is waited out; a long one ends the search, because it is the answer a
// single-model request would have got; the last member's failure ends it as it
// stands. stop is false when the walk goes on to the next member.
func (c *call) settleRetry(ctx context.Context, fail *accountFail, out Output, lastMember bool) (Output, bool) {
	if fail.resetAt.After(c.p.d.Now()) {
		wait := fail.resetAt.Sub(c.p.d.Now())
		if wait > settleCooldown {
			return out, true
		}
		if !sleep(ctx, wait) {
			return c.closed(), true
		}
	}
	return out, lastMember
}

// comboFailure is a member's answer and the error class its row would carry.
type comboFailure struct {
	out   Output
	class string
}

// settle answers with a failure and gives the row its class, not the class of
// a member tried after it.
func (c *call) settle(f *comboFailure) Output {
	c.errClass = f.class
	return f.out
}

// comboMember runs one member to its answer, with its own request, its own
// translate context and its own destination. An empty answer counts as a failure
// for the caller to move past.
func (c *call) comboMember(ctx context.Context, m routing.Member, shape func(*translate.Request)) (Output, *accountFail) {
	// What an earlier member left in the row's status fields is not this one's.
	c.errClass, c.rowStatus = "", 0
	t, err := c.p.d.Resolver.Resolve(ctx, m.Provider+"/"+m.Model)
	if err != nil {
		return Output{}, &accountFail{status: http.StatusBadGateway, text: "The combo member cannot be resolved"}
	}
	if t.Combo != "" {
		return Output{}, &accountFail{status: http.StatusBadGateway, text: "The combo member is a combo"}
	}
	if c.emptyDest(t) {
		return Output{}, &accountFail{status: http.StatusNotFound, class: "not_found", text: "Model not found"}
	}
	dest, out, ok := c.destinationOf(ctx, t)
	if !ok {
		return out, &accountFail{status: out.Status, class: "invalid_request", text: "The combo member cannot be used"}
	}
	c.dest = dest
	// A fresh translate context first: the request is read into it, and the
	// context is filled from the request afterwards.
	c.tctx = translate.NewCtx(c.format, dest.format)
	// The client's request is re-read for this member: building a member's body
	// edits the request in place, so a member never starts from the last one's.
	codec, ok := translate.Lookup(c.format)
	if !ok {
		return Output{}, &accountFail{status: http.StatusInternalServerError, text: "no codec"}
	}
	req, err := codec.ReadRequest(c.in.Body, c.tctx)
	if err != nil {
		return Output{}, &accountFail{status: http.StatusBadRequest, class: "invalid_request", text: "Invalid request body"}
	}
	req.Stream = c.clientStream
	if shape != nil {
		shape(req)
	}
	c.req = req
	c.requested = m.Provider + "/" + m.Model
	c.comboContext(dest, t)

	if out, hit := c.cached(ctx); hit {
		return out, nil
	}
	if out, ok := c.build(ctx, dest); !ok {
		return out, &accountFail{status: out.Status, class: "invalid_request", text: "The combo member cannot be built"}
	}
	out, fail := c.serveFail(ctx)
	if fail != nil {
		return out, fail
	}
	return out, nil
}

// comboContext gives one member a fresh translate context, so nothing the last
// member set leaks into it, and a fresh copy of the client's request, because
// building one member's body edits the request in place.
// comboContext fills the member's translate context from the request that was
// just read into it; the context itself is created before the read.
func (c *call) comboContext(dest destination, t routing.Target) {
	caps := c.p.d.Caps.For(dest.provider, dest.model)
	c.tctx.Provider, c.tctx.Model, c.tctx.Caps, c.tctx.Entry = dest.provider, dest.model, caps, dest.entry
	c.tctx.BudgetOf = c.p.d.Caps.Budget
	if store := newSignatureStore(c.p.d.Signatures, c.in.Caller.ID, dest.model); store != nil {
		c.tctx.Calls = store
	}
	c.tctx.Thinking = translate.ResolveThinking(t.Thinking, c.req.Thinking, translate.ModelThinking{}, caps)
	c.upStream = c.clientStream || dest.entry.ForceStream
}

// comboRefused answers a combo the resolver refused.
func (c *call) comboRefused(ctx context.Context, err error) Output {
	switch {
	case errors.Is(err, routing.ErrComboCycle):
		return c.fail(http.StatusBadRequest, "invalid_request", "The combo names itself")
	case errors.Is(err, routing.ErrComboDepth):
		return c.fail(http.StatusBadRequest, "invalid_request", "The combo is nested too deeply")
	case errors.Is(err, routing.ErrComboEmpty):
		return c.fail(http.StatusBadRequest, "invalid_request", "The combo names no model")
	}
	return c.internal(ctx, err)
}

// comboSession is the conversation this request belongs to, without an account:
// a smart choice sticks to the session, whichever account answers.
func (c *call) comboSession() string {
	if c.p.d.Sessions == nil {
		return ""
	}
	return c.p.d.Sessions.Resolve(conversation.Input{Header: c.in.Header, Body: c.in.Body})
}

// comboState is the latest user text the router is asked about, capped at
// smartStateChars.
func (c *call) comboState() string {
	if c.req == nil {
		return ""
	}
	for i := len(c.req.Messages) - 1; i >= 0; i-- {
		if c.req.Messages[i].Role == translate.RoleUser {
			return apptext.CutRunes(c.req.Messages[i].Text(), smartStateChars)
		}
	}
	return ""
}

// smartStateChars caps the state text the router reads.
const smartStateChars = 2000

// comboCaps is one member's capability row, for the capability order.
func (c *call) comboCaps(m routing.Member) (cap upstream.Capability) {
	return c.p.d.Caps.For(m.Provider, m.Model)
}

// providerOpen reports a provider whose breaker is open, so the member order can
// try it last.
func (c *call) providerOpen(provider string) bool {
	return c.p.d.Health != nil && c.p.d.Health.Open(provider)
}

// comboPolicy reads one combo's strategy from the settings row, cached for the
// same few seconds the rest of the settings are.
func (c *call) comboPolicy(ctx context.Context, name string) (combo.Policy, error) {
	if c.p.d.Settings == nil {
		return combo.Read(document.Doc{}, name), nil
	}
	doc, err := c.p.d.Settings.Effective(ctx)
	if err != nil {
		return combo.Policy{}, err
	}
	return combo.Read(doc, name), nil
}

// expand resolves a combo's members. The resolver owns the expansion; the
// pipeline's interface exposes it through a small assertion so a fake resolver in
// a test can still serve a plain model.
func (c *call) expand(ctx context.Context, name string) ([]routing.Member, error) {
	if e, ok := c.p.d.Resolver.(interface {
		Expand(context.Context, string) ([]routing.Member, error)
	}); ok {
		return e.Expand(ctx, name)
	}
	return nil, routing.ErrComboEmpty
}

// emptyDest reports a target that is a media model, which a chat combo may not
// carry.
func (c *call) emptyDest(t routing.Target) bool {
	m, ok := c.p.d.Registry.Model(t.Provider, t.Model)
	return ok && m.Kind != "" && m.Kind != upstream.KindLLM
}

// memberClass is how a combo treats one member's failure.
type memberClass int

const (
	memberFallback memberClass = iota
	memberRequestScoped
	memberRetry
)

// classifyMember sorts one failure: a request-scoped 4xx moves on and is
// kept as the answer if everything fails; a 502, 503 or 504 with a short reset is
// waited out, a longer one ends the search; any other failure moves on to the
// next member.
func classifyMember(f *accountFail) memberClass {
	switch {
	case f.status >= 400 && f.status <= 499 && f.status != http.StatusUnauthorized &&
		f.status != http.StatusPaymentRequired && f.status != http.StatusForbidden && f.status != http.StatusTooManyRequests:
		return memberRequestScoped
	case f.status == http.StatusBadGateway || f.status == http.StatusServiceUnavailable || f.status == http.StatusGatewayTimeout:
		return memberRetry
	}
	return memberFallback
}

// sleep waits d or until the request ends; false means the request ended.
func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
