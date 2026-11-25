package pipeline

import (
	"context"
	"strings"

	"rllmx/app/gateway/skill"
	"rllmx/app/llm/routing"
	"rllmx/app/llm/upstream"
	"rllmx/app/platform/text"
)

// evaluateRules runs every enabled skill's declarative rules once, before the
// model is resolved. A nil registry, a missing reader or a failed read matches
// nothing, so a rule can only ever leave the request as it was.
func (c *call) evaluateRules(ctx context.Context) {
	c.rules = skill.Actions{}
	if c.in.NoRules || c.p == nil || c.p.d.Skills == nil || c.p.d.Settings == nil {
		return
	}
	// Both skill bypass headers leave the rules off too, so a client asking for
	// an untouched request gets one.
	if c.skillsBypassed() {
		return
	}
	doc, err := c.p.d.Settings.Effective(ctx)
	if err != nil {
		return
	}
	if !doc.Bool("globalSkillsEnabled", true) {
		return
	}
	setting, legacy := skill.Readers(doc.GetRaw)
	off := groupOff(doc)
	enabled := func(m skill.Manifest) bool {
		if off(m.Source) {
			return false
		}
		on, _ := skill.Switch(m, setting, legacy)
		return on
	}
	rules := c.p.d.Skills.Rules(skill.StagePreRoute, enabled)
	rules = append(rules, c.p.d.Skills.Rules(skill.StagePreRequest, enabled)...)
	if len(rules) == 0 {
		return
	}
	c.rules = skill.MatchRules(rules, c.req.Model, c.req.LastUserText(), c.in.Header)
	if !c.rules.Empty() && c.p.d.Log != nil {
		c.p.d.Log.Debugf("rules: %s", strings.Join(c.rules.IDs, "+"))
	}
}

// routeRule rewrites the requested model for a pre-route rule. A restricted key
// must already be allowed the name the client asked for, so a rule cannot turn a
// forbidden model into a permitted one.
func (c *call) routeRule(ctx context.Context) (Output, bool) {
	if c.rules.Reroute == "" || !c.in.Caller.Restricted {
		return Output{}, true
	}
	t, err := c.p.d.Resolver.Resolve(ctx, c.req.Model)
	if err != nil {
		// The name the client sent is not one a key could have been allowed, so
		// the rule's target must not stand in for it: the request goes on with its
		// own name and the ordinary check refuses it.
		c.rules.Reroute = ""
		return Output{}, true
	}
	allow, err := routing.NewAllow(ctx, c.p.d.Resolver, c.in.Caller.Allow)
	if err != nil {
		return c.internal(ctx, err), false
	}
	if !allow.Target(t) {
		return c.refuseModel(), false
	}
	return Output{}, true
}

// applyRulePrompts writes each matched pre-request injection into the request.
// The injector recognises its own earlier segment, so a retry or a combo's next
// member never carries the text twice.
func (c *call) applyRulePrompts() {
	for _, prompt := range c.rules.Prompts {
		c.req.AppendSystem(prompt)
	}
}

// applyRuleHeaders merges a pre-request rule's headers over the operator's
// overrides. Each is filtered again here because an executor may apply an
// override without the shared header guard.
func applyRuleHeaders(overrides map[string]string, headers [][2]string, authHeader string) map[string]string {
	if len(headers) == 0 {
		return overrides
	}
	out := make(map[string]string, len(overrides)+len(headers))
	for k, v := range overrides {
		out[k] = v
	}
	for _, h := range headers {
		name, value := h[0], h[1]
		if _, set := out[name]; set {
			continue
		}
		if !upstream.HeaderName(name) || upstream.ReservedHeader(name) || text.HasASCIIControl(value) {
			continue
		}
		if authHeader != "" && strings.EqualFold(name, authHeader) {
			continue
		}
		out[name] = value
	}
	return out
}
