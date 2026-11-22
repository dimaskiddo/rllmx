package skill

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"rllmx/app/llm/upstream"
	"rllmx/app/platform/text"
)

// Bounds on a rules file and the action it may take, so a crafted skill cannot
// make the request path allocate or rewrite a message without limit.
const (
	maxRulesFile = 16 << 10
	maxRules     = 20
	maxPattern   = 512
	maxReroute   = 256
	maxInject    = 4 << 10
	maxHeaderVal = 1 << 10
	maxActions   = 8
	// matchMessage is how much of the last user message a rule sees.
	matchMessage = 64 << 10
)

// A rule's stage is also the hook its manifest must name, so a rule file and the
// manifest that carries it cannot disagree about when it runs.
const (
	StagePreRoute   = "pre-route"
	StagePreRequest = "pre-request"
)

// Rule is one compiled declarative hook. Its matchers and its actions are fixed
// at load time, so a request only runs regexps.
type Rule struct {
	ID    string
	stage string

	model  *regexp.Regexp
	msg    *regexp.Regexp
	header string
	hregex *regexp.Regexp

	reroute   string
	inject    string
	setName   string
	setValue  string
	hasHeader bool
}

// Actions is what a request's matched rules do, in rule order.
type Actions struct {
	// IDs names the skills whose rules matched, for the log line and the cache
	// key.
	IDs []string
	// Reroute is the first model a pre-route rule names.
	Reroute string
	// Prompts is every pre-request injection, in rule order.
	Prompts []string
	// Headers is every upstream header, first value per name.
	Headers [][2]string
}

// Empty reports that no rule matched.
func (a Actions) Empty() bool { return len(a.IDs) == 0 }

type ruleDoc struct {
	Rules []ruleWire `json:"rules"`
}

type ruleWire struct {
	Stage string `json:"stage"`
	When  struct {
		ModelRegex   string `json:"modelRegex,omitempty"`
		MessageRegex string `json:"messageRegex,omitempty"`
		Header       *struct {
			Name  string `json:"name"`
			Regex string `json:"regex"`
		} `json:"header,omitempty"`
	} `json:"when"`
	Then struct {
		InjectPrompt string `json:"injectPrompt,omitempty"`
		RerouteModel string `json:"rerouteModel,omitempty"`
		SetHeader    *struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"setHeader,omitempty"`
	} `json:"then"`
}

// rulesFrom parses and compiles a rules file. hook is the manifest's hook; every
// rule must name it.
func rulesFrom(raw []byte, id, hook string) ([]Rule, error) {
	var file ruleDoc
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("rules.json: %w", err)
	}
	if len(file.Rules) > maxRules {
		return nil, fmt.Errorf("rules.json holds %d rules, the cap is %d", len(file.Rules), maxRules)
	}
	out := make([]Rule, 0, len(file.Rules))
	for i, w := range file.Rules {
		r, err := compileRule(w, id)
		if err != nil {
			return nil, fmt.Errorf("rules.json rule %d: %w", i+1, err)
		}
		if r.stage != hook {
			return nil, fmt.Errorf("rules.json rule %d is %s, but the manifest's hook is %s", i+1, r.stage, hook)
		}
		out = append(out, r)
	}
	return out, nil
}

// compileRule checks one rule and compiles its matchers. Every refusal names
// the field, so a dashboard can say what to fix.
func compileRule(w ruleWire, id string) (Rule, error) {
	r := Rule{ID: id, stage: w.Stage}
	if r.stage != StagePreRoute && r.stage != StagePreRequest {
		return r, fmt.Errorf("stage %q must be pre-route or pre-request", w.Stage)
	}
	when := w.When
	if when.ModelRegex == "" && when.MessageRegex == "" && when.Header == nil {
		return r, fmt.Errorf("when must match the model, the message or a header")
	}
	var err error
	if when.ModelRegex != "" {
		if r.model, err = compile(when.ModelRegex); err != nil {
			return r, fmt.Errorf("modelRegex %w", err)
		}
	}
	if when.MessageRegex != "" {
		if r.msg, err = compile(when.MessageRegex); err != nil {
			return r, fmt.Errorf("messageRegex %w", err)
		}
	}
	if when.Header != nil {
		if !matchHeader(when.Header.Name) {
			return r, fmt.Errorf("header %q is not a request header a rule may read", when.Header.Name)
		}
		r.header = http.CanonicalHeaderKey(when.Header.Name)
		if r.hregex, err = compile(when.Header.Regex); err != nil {
			return r, fmt.Errorf("header regex %w", err)
		}
	}

	then := w.Then
	switch r.stage {
	case StagePreRoute:
		if then.InjectPrompt != "" || then.SetHeader != nil {
			return r, fmt.Errorf("a pre-route rule may only rerouteModel")
		}
		if then.RerouteModel == "" {
			return r, fmt.Errorf("a pre-route rule needs rerouteModel")
		}
		if len(then.RerouteModel) > maxReroute || text.HasControl(then.RerouteModel) {
			return r, fmt.Errorf("rerouteModel must be 1-%d printable characters", maxReroute)
		}
		r.reroute = then.RerouteModel
	case StagePreRequest:
		if then.RerouteModel != "" {
			return r, fmt.Errorf("a pre-request rule may not rerouteModel")
		}
		if then.InjectPrompt == "" && then.SetHeader == nil {
			return r, fmt.Errorf("a pre-request rule needs injectPrompt or setHeader")
		}
		if len(then.InjectPrompt) > maxInject || text.HasControl(then.InjectPrompt) {
			return r, fmt.Errorf("injectPrompt must be at most %d printable bytes", maxInject)
		}
		r.inject = then.InjectPrompt
		if then.SetHeader != nil {
			name, value := then.SetHeader.Name, then.SetHeader.Value
			if deniedHeader(name) {
				return r, fmt.Errorf("setHeader may not set %q", name)
			}
			if !upstream.HeaderName(name) {
				return r, fmt.Errorf("setHeader name %q is not a header name", name)
			}
			if len(value) > maxHeaderVal || text.HasASCIIControl(value) {
				return r, fmt.Errorf("setHeader value must be at most %d printable bytes", maxHeaderVal)
			}
			r.setName, r.setValue, r.hasHeader = name, value, true
		}
	}
	return r, nil
}

// compile bounds a pattern before compiling it, so an oversized rule is refused
// rather than handed to the regexp engine.
func compile(p string) (*regexp.Regexp, error) {
	if len(p) > maxPattern {
		return nil, fmt.Errorf("pattern is over %d bytes", maxPattern)
	}
	re, err := regexp.Compile(p)
	if err != nil {
		return nil, err
	}
	return re, nil
}

// deniedHeader reports a header no rule may set. upstream.ReservedHeader covers
// the framing and credential headers the executor already refuses.
func deniedHeader(name string) bool {
	switch strings.ToLower(name) {
	case "proxy-authorization", "proxy-authenticate", "te", "trailer", "upgrade":
		return true
	}
	return upstream.ReservedHeader(name)
}

// matchHeader reports a request header a rule may match on. The pipeline copies
// only these into a request, so a rule can never read a client header the
// pipeline itself does not keep.
func matchHeader(name string) bool {
	switch strings.ToLower(name) {
	case "user-agent", "x-app", "x-client", "originator", "anthropic-beta", "openai-intent", "x-initiator",
		"x-claude-code-session-id", "x-session-id", "session-id", "session_id", "x-amp-thread-id",
		"x-client-request-id", "x-9router-token-saver", BypassLocal, BypassECC:
		return true
	}
	return false
}

// Rules returns the compiled rules of the enabled skills that run at stage, in
// id order.
func (r *Registry) Rules(stage string, enabled func(Manifest) bool) []Rule {
	if r == nil {
		return nil
	}
	var out []Rule
	for _, e := range r.refresh().entries {
		if len(e.rules) == 0 || e.m.Hook != stage {
			continue
		}
		if enabled != nil && !enabled(e.m) {
			continue
		}
		out = append(out, e.rules...)
	}
	return out
}

// MatchRules runs the rules against one request and collects their actions. A
// rule matches when every matcher it names matches; the first reroute wins, the
// first value of a header wins, and at most maxActions headers are set.
func MatchRules(rules []Rule, model, message string, h http.Header) Actions {
	var a Actions
	if len(rules) == 0 {
		return a
	}
	message = text.CutBytes(message, matchMessage)
	seenHeader := map[string]bool{}
	seenID := map[string]bool{}
	for _, r := range rules {
		if !r.matches(model, message, h) {
			continue
		}
		if !seenID[r.ID] {
			seenID[r.ID] = true
			a.IDs = append(a.IDs, r.ID)
		}
		if a.Reroute == "" {
			a.Reroute = r.reroute
		}
		if r.inject != "" {
			a.Prompts = append(a.Prompts, r.inject)
		}
		if r.hasHeader && len(a.Headers) < maxActions {
			name := http.CanonicalHeaderKey(r.setName)
			if !seenHeader[name] {
				seenHeader[name] = true
				a.Headers = append(a.Headers, [2]string{name, r.setValue})
			}
		}
	}
	return a
}

func (r Rule) matches(model, message string, h http.Header) bool {
	if r.model != nil && !r.model.MatchString(model) {
		return false
	}
	if r.msg != nil && !r.msg.MatchString(message) {
		return false
	}
	if r.hregex != nil {
		if !r.hregex.MatchString(h.Get(r.header)) {
			return false
		}
	}
	return true
}
