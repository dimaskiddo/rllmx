package pipeline

import (
	"context"
	"errors"
	"strings"
	"time"

	"rllmx/app/gateway/plan"
	"rllmx/app/llm/routing"
	"rllmx/app/llm/stream"
	"rllmx/app/llm/translate"
	"rllmx/app/store/repository"
)

// errNoStream is what an internal call's opener returns: a plan is never
// streamed to a client.
var errNoStream = errors.New("an internal call never streams")

// maxJudgeTokens bounds the classifier answer.
const maxJudgeTokens = 20

// autoPlan asks a planning combo for an architecture plan when the turn looks
// like a build task, then puts the plan in front of the last user turn and,
// when a code combo is configured, moves the request there. ok is true only
// when the request is already answered (a spent budget); every other early
// return leaves the request exactly as it arrived.
func (c *call) autoPlan(ctx context.Context) (Output, bool) {
	if c.in.Internal || c.p == nil || c.p.d.Settings == nil || c.p.d.Resolver == nil || c.req == nil {
		return Output{}, false
	}
	if c.headerOff(plan.BypassHeader) {
		return Output{}, false
	}
	doc, err := c.p.d.Settings.Effective(ctx)
	if err != nil {
		return Output{}, false
	}
	s := plan.Read(doc)
	if !s.Enabled {
		return Output{}, false
	}
	// A spent key is refused before it pays for a plan.
	if out, ok := c.budget(ctx); !ok {
		return out, true
	}
	planCombo, codeCombo, ok := c.autoPlanCombos(ctx, s)
	if !ok {
		return Output{}, false
	}
	// A restricted key that may not call the planning combo, or the combo the
	// code would move to, plans nothing: it is not refused for a model it never
	// named, and it does not pay for a plan it cannot use.
	if !c.reachable(ctx, planCombo) || (codeCombo != "" && !c.reachable(ctx, codeCombo)) {
		return Output{}, false
	}
	v := plan.Decide(ctx, c.req, s, c.planJudge(planCombo, s))
	if !v.NeedsPlan {
		return Output{}, false
	}
	ans, ok := c.runPlan(ctx, planCombo, s)
	if !ok {
		return Output{}, false
	}
	body := strings.TrimSpace(ans.Message.Text())
	if body == "" {
		return Output{}, false
	}
	plan.Inject(c.req, body)
	c.planInjected, c.planTurn = true, body
	if codeCombo != "" && codeCombo != c.req.Model {
		c.req.Model, c.requested = codeCombo, codeCombo
	}
	if c.p.d.Log != nil {
		c.p.d.Log.Debugf("plan: %s tokens=%d combo=%q", v, ans.Usage.Completion, planCombo)
	}
	return Output{}, false
}

// comboLister is the resolver's ordered combo list. A resolver without it runs
// no auto plan, so a fake stays a plain resolver.
type comboLister interface {
	ComboList(ctx context.Context) ([]repository.Combo, error)
}

// autoPlanCombos chooses the two combos and checks the planning one has a
// member to call.
func (c *call) autoPlanCombos(ctx context.Context, s plan.Settings) (string, string, bool) {
	lister, ok := c.p.d.Resolver.(comboLister)
	if !ok {
		return "", "", false
	}
	rows, err := lister.ComboList(ctx)
	if err != nil || len(rows) == 0 {
		return "", "", false
	}
	list := make([]plan.Combo, 0, len(rows))
	for _, r := range rows {
		list = append(list, plan.Combo{ID: r.ID, Name: r.Name, Members: r.Models})
	}
	planCombo, codeCombo := plan.Combos(list, s, c.req.Model)
	if planCombo == "" {
		return "", "", false
	}
	members, err := c.expand(ctx, planCombo)
	if err != nil || len(members) == 0 {
		return "", "", false
	}
	return planCombo, codeCombo, true
}

// reachable reports whether a restricted key's list covers a combo, resolved
// the way a request resolves it.
func (c *call) reachable(ctx context.Context, name string) bool {
	if !c.in.Caller.Restricted {
		return true
	}
	t, err := c.p.d.Resolver.Resolve(ctx, name)
	if err != nil {
		return false
	}
	allow, err := routing.NewAllow(ctx, c.p.d.Resolver, c.in.Caller.Allow)
	if err != nil {
		return false
	}
	return allow.Target(t)
}

// runPlan sends the conversation to the planning combo under the architect
// prompt, with no tools, so what comes back is text.
func (c *call) runPlan(ctx context.Context, combo string, s plan.Settings) (translate.Response, bool) {
	n := s.MaxTokens
	req := &translate.Request{
		Model:    combo,
		Messages: plan.PlanMessages(c.req),
		Params:   translate.Params{MaxTokens: &n},
	}
	return c.internalCall(ctx, req, "-plan", s)
}

// planJudge asks the planning combo the YES/NO question, by its name: a key that
// may call the combo may not call one of its members. A failed call is reported,
// so the classifier keeps its heuristic verdict.
func (c *call) planJudge(combo string, s plan.Settings) plan.Judge {
	return func(ctx context.Context, question string) (string, bool) {
		n, temp := maxJudgeTokens, 0.1
		req := &translate.Request{
			Model:    combo,
			Messages: []translate.Message{{Role: translate.RoleUser, Content: translate.TextPart(question)}},
			Params:   translate.Params{MaxTokens: &n, Temperature: &temp},
		}
		ans, ok := c.internalCall(ctx, req, "-classify", s)
		if !ok {
			return "", false
		}
		return ans.Message.Text(), true
	}
}

// internalCall runs one request of this router's own making through the same
// pipeline, in the OpenAI format whatever the client speaks, and reads the
// answer back in it. The call carries no tools, no rules, no skills and no
// plan of its own, and its context is bounded by the plan timeout.
func (c *call) internalCall(ctx context.Context, req *translate.Request, suffix string, s plan.Settings) (translate.Response, bool) {
	codec, ok := translate.Lookup(translate.FormatOpenAI)
	if !ok {
		return translate.Response{}, false
	}
	req.Stream = false
	tctx := translate.NewCtx(translate.FormatOpenAI, translate.FormatOpenAI)
	body, err := codec.WriteRequest(req, tctx)
	if err != nil {
		return translate.Response{}, false
	}
	rctx, cancel := context.WithTimeout(ctx, time.Duration(s.TimeoutMS)*time.Millisecond)
	defer cancel()
	out := c.p.Handle(rctx, Input{
		Path:       "/v1/chat/completions",
		Body:       body,
		Caller:     c.in.Caller,
		RequestID:  c.in.RequestID + suffix,
		UserAgent:  c.in.UserAgent,
		Header:     c.internalHeader(),
		AcceptJSON: true,
		Internal:   true,
		NoRules:    true,
	}, func() (stream.Client, error) { return nil, errNoStream })
	if out.Status < 200 || out.Status > 299 {
		return translate.Response{}, false
	}
	ans, err := codec.ReadResponse(out.Body, tctx)
	if err != nil || ans == nil {
		return translate.Response{}, false
	}
	return *ans, true
}
