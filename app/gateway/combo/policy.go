// Package combo decides the order a combo's members are tried in: the
// strategy its settings name, the capability the request needs, and the member a
// router or the session chose last time.
package combo

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/routing"
	"rllmx/app/llm/translate"
	"rllmx/app/llm/upstream"
	"rllmx/app/store/document"
)

// Strategies a combo may name.
const (
	StrategyFallback   = "fallback"
	StrategyRoundRobin = "round-robin"
	StrategySmart      = "smart"
	StrategyFusion     = "fusion"
)

// Defaults for an entry that names none of these.
const (
	DefaultStrategy    = StrategyFallback
	DefaultThreshold   = 0.8
	DefaultMinPanel    = 2
	DefaultStragglerMS = 8000
	DefaultPanelMS     = 90000
	// defaultRouterModel is the free model a smart combo asks for a choice when
	// the combo names none.
	defaultRouterModel = "oc/jev-1.13-free"
)

// Policy is one combo's strategy and its knobs.
type Policy struct {
	Name     string
	Strategy string
	// Smart knobs, from the per-combo entry only.
	SmartPolicy string
	Sticky      bool
	Threshold   float64
	RouterModel string
	JudgeModel  string
	// Fusion knobs.
	MinPanel    int
	StragglerMS int
	PanelMS     int
	// RotationLimit is how many requests a round-robin start serves before it
	// advances.
	RotationLimit int
}

// Read is one combo's policy from the settings document: the per-combo entry,
// then the global strategy, then fallback. Smart's knobs live only in the
// per-combo entry only, never as a top-level key.
func Read(doc document.Doc, name string) Policy {
	p := Policy{Name: name, Strategy: DefaultStrategy, Sticky: true, Threshold: DefaultThreshold, RouterModel: defaultRouterModel}
	p.RotationLimit = rotationLimit(doc)
	if s, _ := doc.GetString("comboStrategy"); strings.TrimSpace(s) != "" {
		p.Strategy = s
	}
	entry := gjson.Get(doc.String(), "comboStrategies."+escapePath(name))
	if !entry.IsObject() {
		p.Strategy = normalise(p.Strategy)
		return p
	}
	if s := strings.TrimSpace(entry.Get("fallbackStrategy").String()); s != "" {
		p.Strategy = s
	}
	p.Strategy = normalise(p.Strategy)
	if s := strings.TrimSpace(entry.Get("smartPolicy").String()); s != "" {
		p.SmartPolicy = s
	}
	if v := entry.Get("sessionSticky"); v.Type == gjson.False {
		p.Sticky = false
	}
	if v := entry.Get("confidenceThreshold"); v.Type == gjson.Number && v.Float() > 0 {
		p.Threshold = v.Float()
	} else if p.SmartPolicy == "smart-switch-70" {
		p.Threshold = 0.7
	} else if p.SmartPolicy == "smart-switch-90" {
		p.Threshold = 0.9
	}
	if s := strings.TrimSpace(entry.Get("routerModel").String()); s != "" {
		p.RouterModel = s
	}
	p.JudgeModel = strings.TrimSpace(entry.Get("judgeModel").String())
	if v := entry.Get("fusionTuning.minPanel"); v.Type == gjson.Number && v.Int() > 0 {
		p.MinPanel = int(v.Int())
	}
	if v := entry.Get("fusionTuning.stragglerGraceMs"); v.Type == gjson.Number && v.Int() > 0 {
		p.StragglerMS = int(v.Int())
	}
	if v := entry.Get("fusionTuning.panelHardTimeoutMs"); v.Type == gjson.Number && v.Int() > 0 {
		p.PanelMS = int(v.Int())
	}
	if p.MinPanel < 1 {
		p.MinPanel = DefaultMinPanel
	}
	if p.StragglerMS < 1 {
		p.StragglerMS = DefaultStragglerMS
	}
	if p.PanelMS < 1 {
		p.PanelMS = DefaultPanelMS
	}
	return p
}

// normalise maps an unknown strategy to fallback, so a typo never refuses a
// request the operator meant to serve.
func normalise(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case StrategyRoundRobin, StrategySmart, StrategyFusion, StrategyFallback:
		return s
	}
	return DefaultStrategy
}

// rotationLimit is the round-robin sticky count; a value below one reads as one.
func rotationLimit(doc document.Doc) int {
	v := gjson.Get(doc.String(), "comboStickyRoundRobinLimit")
	if v.Type != gjson.Number || v.Int() < 1 {
		return 1
	}
	return int(v.Int())
}

// escapePath quotes a name for a gjson path, so a dot in a combo name is part of
// the name rather than a step.
func escapePath(name string) string {
	r := strings.NewReplacer(`\`, `\\`, `.`, `\.`, `*`, `\*`, `?`, `\?`, `|`, `\|`, `#`, `\#`, `@`, `\@`)
	return r.Replace(name)
}

// Caps is what one member can do, for the capability order.
type Caps func(m routing.Member) upstream.Capability

// Order is the member order a request is tried in. Capability comes first for
// every strategy: a member that cannot read what the turn carries is last, however
// the strategy would place it. Then the strategy reorders what is left.
func Order(p Policy, members []routing.Member, caps Caps, needs translate.Needs, state *Rotation, smart *Smart) ([]routing.Member, bool) {
	if len(members) == 0 {
		return nil, false
	}
	out := byCapability(members, caps, needs)
	switch p.Strategy {
	case StrategyRoundRobin:
		if state != nil {
			out = state.Rotate(p.Name, out, p.RotationLimit)
		}
	case StrategySmart:
		out = p.smartOrder(out, smart)
	}
	return out, true
}

// Smart is what a smart strategy needs: the router that names a member, the
// session's last choice and the text of the current turn. A nil Smart, or a nil
// Router inside it, means no router: the declared order (or the sticky member)
// stands, as it does when the router fails.
type Smart struct {
	Router  Router
	Sticky  *Sticky
	Session string
	State   string
	Now     func() time.Time
}

// sticky reports whether a session's last choice is remembered: per-turn asks
// every time: sessionSticky:false or smartPolicy:"per-turn".
func (p Policy) sticky() bool { return p.Sticky && p.SmartPolicy != "per-turn" }

// strict reports a policy that never re-asks while a sticky choice is remembered.
func (p Policy) strict() bool { return p.SmartPolicy == "strict" }

// smartOrder applies the smart strategy: a remembered choice leads while the
// policy keeps it, otherwise the router names a member and its answer is
// remembered. Every failure keeps the order the policy would otherwise use.
func (p Policy) smartOrder(members []routing.Member, smart *Smart) []routing.Member {
	if smart == nil || len(members) == 0 {
		return members
	}
	now := time.Now()
	if smart.Now != nil {
		now = smart.Now()
	}
	sticky := ""
	if p.sticky() {
		sticky = smart.Sticky.Get(p.Name, smart.Session, now)
	}
	inList := func(model string) bool {
		for _, m := range members {
			if m.Model == model {
				return true
			}
		}
		return false
	}
	stickyFirst := func() []routing.Member {
		for i, m := range members {
			if m.Model == sticky {
				out := make([]routing.Member, 0, len(members))
				out = append(out, m)
				out = append(out, members[:i]...)
				return append(out, members[i+1:]...)
			}
		}
		return members
	}
	// A remembered choice leads while the policy keeps it; a per-turn policy
	// re-asks every time and a strict one never does.
	if sticky != "" && inList(sticky) && (p.strict() || smart.Router == nil) {
		return stickyFirst()
	}
	if smart.Router == nil {
		return members
	}
	models := make([]string, len(members))
	for i, m := range members {
		models[i] = m.Model
	}
	choice, err := smart.Router.Choose(context.Background(), smart.State, models)
	if err != nil || !inList(choice.Model) || choice.Confidence < p.Threshold {
		// The router failed, named a model the combo does not have, or was not
		// confident enough: the sticky choice (if any) keeps the lead.
		if sticky != "" && inList(sticky) {
			return stickyFirst()
		}
		return members
	}
	if p.sticky() {
		smart.Sticky.Put(p.Name, smart.Session, choice.Model, now)
	}
	if choice.Model == sticky {
		return stickyFirst()
	}
	for i, m := range members {
		if m.Model == choice.Model {
			out := make([]routing.Member, 0, len(members))
			out = append(out, m)
			out = append(out, members[:i]...)
			return append(out, members[i+1:]...)
		}
	}
	return members
}

// byCapability keeps the declared order within three tiers: a member that meets
// every need, one that meets the hard needs (the same set here), and the rest.
func byCapability(members []routing.Member, caps Caps, needs translate.Needs) []routing.Member {
	if !needs.Any() || caps == nil {
		return slices.Clone(members)
	}
	out := make([]routing.Member, 0, len(members))
	for round := 0; round < 2; round++ {
		for _, m := range members {
			met := needs.Met(caps(m))
			if (round == 0) == met {
				out = append(out, m)
			}
		}
	}
	return out
}
