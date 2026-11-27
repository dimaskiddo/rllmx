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
	"rllmx/app/platform/text"
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
	p := Policy{
		Name: name, Strategy: DefaultStrategy, Sticky: true, Threshold: DefaultThreshold, RouterModel: defaultRouterModel,
		MinPanel: DefaultMinPanel, StragglerMS: DefaultStragglerMS, PanelMS: DefaultPanelMS,
	}
	p.RotationLimit = rotationLimit(doc)
	if s, _ := doc.GetString("comboStrategy"); strings.TrimSpace(s) != "" {
		p.Strategy = s
	}
	entry := gjson.Get(doc.String(), "comboStrategies."+text.EscapePath(name))
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

// Caps is what one member can do, for the capability order.
type Caps func(m routing.Member) upstream.Capability

// Order is the member order a request is tried in. Capability comes first for
// every strategy: a member that cannot read what the turn carries is last, however
// the strategy would place it. Then the strategy reorders what is left, and a
// provider whose breaker is open goes to the end.
func Order(ctx context.Context, p Policy, members []routing.Member, caps Caps, open func(provider string) bool, needs translate.Needs, state *Rotation, smart *Smart) ([]routing.Member, bool) {
	if len(members) == 0 {
		return nil, false
	}
	out, capable := byCapability(members, caps, needs)
	switch p.Strategy {
	case StrategyRoundRobin:
		if state != nil {
			// Only the members that meet the need take turns; the rest stay behind them.
			out = append(state.Rotate(p.Name, out[:capable:capable], p.RotationLimit), out[capable:]...)
		}
	case StrategySmart:
		// The router reorders the members that can read the turn; one that
		// cannot stays behind them, where the walk reaches it only if every
		// capable member failed.
		if capable > 0 {
			out = append(p.smartOrder(ctx, out[:capable:capable], smart), out[capable:]...)
		} else {
			out = p.smartOrder(ctx, out, smart)
		}
	}
	// An open breaker moves a member behind the rest, never across the
	// capability line: the only model that can read the turn is not sent last
	// behind ones that cannot.
	return append(byHealth(out[:capable:capable], open), byHealth(out[capable:], open)...), true
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
func (p Policy) smartOrder(ctx context.Context, members []routing.Member, smart *Smart) []routing.Member {
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
	choice, err := smart.Router.Choose(ctx, p.RouterModel, smart.State, models)
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

// byCapability keeps the declared order with the members that meet every need
// first, and says how many those are.
func byCapability(members []routing.Member, caps Caps, needs translate.Needs) ([]routing.Member, int) {
	if !needs.Any() || caps == nil {
		return slices.Clone(members), len(members)
	}
	out := partition(members, func(m routing.Member) bool { return !needs.Met(caps(m)) })
	capable := 0
	for _, m := range members {
		if needs.Met(caps(m)) {
			capable++
		}
	}
	return out, capable
}

// byHealth moves a member whose provider's breaker is open to the end, keeping
// the order inside both halves. Nothing is dropped: an open provider still
// answers when it is all that is left.
func byHealth(members []routing.Member, open func(provider string) bool) []routing.Member {
	if open == nil {
		return members
	}
	return partition(members, func(m routing.Member) bool { return open(m.Provider) })
}

// partition keeps the order and sends the members last reports true on to the
// end.
func partition(members []routing.Member, last func(routing.Member) bool) []routing.Member {
	out := make([]routing.Member, 0, len(members))
	for round := 0; round < 2; round++ {
		for _, m := range members {
			if (round == 1) == last(m) {
				out = append(out, m)
			}
		}
	}
	return out
}
