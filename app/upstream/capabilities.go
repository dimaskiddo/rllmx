package upstream

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

//go:embed data/capabilities/capabilities.json
var embeddedCapabilities []byte

//go:embed data/capabilities/thinking.json
var embeddedThinking []byte

//go:embed data/capabilities/vision.json
var embeddedVision []byte

//go:embed data/capabilities/params.json
var embeddedParams []byte

const maxTableBytes = 1 << 20

// Capability is what a model can read and do beyond plain text.
type Capability struct {
	Vision      bool `json:"vision"`
	PDF         bool `json:"pdf"`
	AudioInput  bool `json:"audioInput"`
	VideoInput  bool `json:"videoInput"`
	ImageOutput bool `json:"imageOutput"`
	AudioOutput bool `json:"audioOutput"`
	Search      bool `json:"search"`
	Tools       bool `json:"tools"`
	Reasoning   bool `json:"reasoning"`
	// ThinkingFormat is how a thinking request is written on the wire, or empty
	// to follow the transport format.
	ThinkingFormat string `json:"thinkingFormat,omitempty"`
	ThinkingCanOff bool   `json:"thinkingCanDisable"`
	// ThinkingEffortSupported is set for a zai-format model that reads a
	// reasoning_effort level beside its thinking switch.
	ThinkingEffortSupported bool     `json:"thinkingEffortSupported,omitempty"`
	ThinkingMin             int      `json:"thinkingMin,omitempty"`
	ThinkingMax             int      `json:"thinkingMax,omitempty"`
	ContextWindow           int      `json:"contextWindow"`
	MaxOutput               int      `json:"maxOutput"`
	ThinkingOffType         string   `json:"thinkingOffType,omitempty"`
	RelaxToolChoice         bool     `json:"relaxToolChoice,omitempty"`
	DropOutputConfig        bool     `json:"dropOutputConfig,omitempty"`
	KeepContextMgmt         bool     `json:"keepContextManagement,omitempty"`
	ClaudeToolTypes         []string `json:"claudeSupportedToolTypes,omitempty"`
}

// defaultCapability is the floor every lookup is merged over.
func defaultCapability() Capability {
	return Capability{Tools: true, ThinkingCanOff: true, ContextWindow: 200000, MaxOutput: 64000}
}

type capRule struct {
	Provider string          `json:"provider,omitempty"`
	Pattern  string          `json:"pattern"`
	Set      json.RawMessage `json:"set"`
}

type capabilityFile struct {
	Providers map[string]map[string]json.RawMessage `json:"providers"`
	Models    map[string]json.RawMessage            `json:"models"`
	Patterns  []capRule                             `json:"patterns"`
}

type compiledRule struct {
	provider string
	re       *regexp.Regexp
	set      json.RawMessage
}

// ThinkingRule overrides the selectable levels for a model glob.
type thinkingRule struct {
	Provider string   `json:"provider,omitempty"`
	Pattern  string   `json:"pattern"`
	Levels   []string `json:"levels"`
}

type thinkingFile struct {
	// Formats maps a thinking format to its selectable levels.
	Formats map[string][]string `json:"formats"`
	Base    []string            `json:"base"`
	Rules   []thinkingRule      `json:"rules"`
	// Budgets maps a level to a token budget.
	Budgets map[string]int `json:"budgets"`
	// ClaudeSignature and GeminiSignature are the placeholder signatures sent
	// for a thinking block or a first tool call that carries none.
	ClaudeSignature string `json:"claudeSignature"`
	GeminiSignature string `json:"geminiSignature"`
}

type visionFile struct {
	Not  []string `json:"not"`
	Name []string `json:"name"`
}

// ParamRule drops or renames request fields for matching models. The translator
// applies them last, to the final body.
type ParamRule struct {
	Provider string `json:"provider,omitempty"`
	Pattern  string `json:"pattern,omitempty"`
	// Exclude is a glob a model must NOT match for the rule to apply.
	Exclude string `json:"exclude,omitempty"`
	// Drop lists top-level body fields to remove.
	Drop []string `json:"drop,omitempty"`
	// Rename maps a top-level field to its new name.
	Rename map[string]string `json:"rename,omitempty"`
	// DropMessageFields removes fields from every assistant message.
	DropMessageFields []string `json:"dropMessageFields,omitempty"`
	// FlattenContent turns a text-only content array into a string.
	FlattenContent bool `json:"flattenContent,omitempty"`
	// CapOutput limits max_tokens; zero means no cap.
	CapOutput int `json:"capOutput,omitempty"`
}

type paramsFile struct {
	Rules []ParamRule `json:"rules"`
}

type paramCompiled struct {
	ParamRule
	re      *regexp.Regexp
	exclude *regexp.Regexp
}

// Capabilities resolves capability, thinking, vision and parameter rules.
type Capabilities struct {
	caps     capabilityFile
	capRules []compiledRule
	thinking thinkingFile
	thinkRe  []*regexp.Regexp
	visionNo *regexp.Regexp
	visionOk *regexp.Regexp
	params   []paramCompiled
	registry *Registry
}

// LoadCapabilities reads the embedded tables. A bad table is an error.
func LoadCapabilities(reg *Registry) (*Capabilities, error) {
	return NewCapabilities(embeddedCapabilities, embeddedThinking, embeddedVision, embeddedParams, reg)
}

func strictDecode(name string, data []byte, v any) error {
	if len(data) > maxTableBytes {
		return fmt.Errorf("upstream: %s is over %d bytes", name, maxTableBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("upstream: %s: %s", name, decodeReason(err))
	}
	if dec.More() {
		return fmt.Errorf("upstream: %s: more than one JSON value", name)
	}
	return nil
}

// NewCapabilities builds the resolver from table text, so a test can pass its own.
func NewCapabilities(capData, thinkData, visionData, paramData []byte, reg *Registry) (*Capabilities, error) {
	c := &Capabilities{registry: reg}
	if err := strictDecode("capabilities.json", capData, &c.caps); err != nil {
		return nil, err
	}
	if err := strictDecode("thinking.json", thinkData, &c.thinking); err != nil {
		return nil, err
	}
	var v visionFile
	if err := strictDecode("vision.json", visionData, &v); err != nil {
		return nil, err
	}
	var p paramsFile
	if err := strictDecode("params.json", paramData, &p); err != nil {
		return nil, err
	}
	probe := defaultCapability()
	check := func(raw json.RawMessage) error {
		cp := probe
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&cp); err != nil {
			return errors.New(decodeReason(err))
		}
		if cp.MaxOutput < 0 || cp.ContextWindow < 0 || cp.ThinkingMin < 0 || cp.ThinkingMax < cp.ThinkingMin {
			return errors.New("a limit is negative or a range is inverted")
		}
		return nil
	}
	for prov, models := range c.caps.Providers {
		for model, raw := range models {
			if err := check(raw); err != nil {
				return nil, fmt.Errorf("upstream: capabilities.json: provider %q model %q: %w", prov, model, err)
			}
		}
	}
	for model, raw := range c.caps.Models {
		if err := check(raw); err != nil {
			return nil, fmt.Errorf("upstream: capabilities.json: model %q: %w", model, err)
		}
	}
	for i, r := range c.caps.Patterns {
		if r.Pattern == "" {
			return nil, fmt.Errorf("upstream: capabilities.json: pattern %d is empty", i)
		}
		if err := check(r.Set); err != nil {
			return nil, fmt.Errorf("upstream: capabilities.json: pattern %q: %w", r.Pattern, err)
		}
		c.capRules = append(c.capRules, compiledRule{provider: r.Provider, re: compileGlob(r.Pattern), set: r.Set})
	}
	for i, r := range c.thinking.Rules {
		if r.Pattern == "" || len(r.Levels) == 0 {
			return nil, fmt.Errorf("upstream: thinking.json: rule %d needs a pattern and levels", i)
		}
		c.thinkRe = append(c.thinkRe, compileGlob(r.Pattern))
	}
	for level, n := range c.thinking.Budgets {
		if n < 0 || n > 1_000_000 {
			return nil, fmt.Errorf("upstream: thinking.json: budget for %q is out of range", level)
		}
	}
	if c.thinking.ClaudeSignature == "" || c.thinking.GeminiSignature == "" {
		return nil, errors.New("upstream: thinking.json: both placeholder signatures are required")
	}
	var err error
	if c.visionNo, err = joinRegexp("vision.json not", v.Not); err != nil {
		return nil, err
	}
	if c.visionOk, err = joinRegexp("vision.json name", v.Name); err != nil {
		return nil, err
	}
	for i, r := range p.Rules {
		if len(r.Drop) == 0 && len(r.Rename) == 0 && len(r.DropMessageFields) == 0 && !r.FlattenContent && r.CapOutput == 0 {
			return nil, fmt.Errorf("upstream: params.json: rule %d does nothing", i)
		}
		if r.CapOutput < 0 {
			return nil, fmt.Errorf("upstream: params.json: rule %d has a negative cap", i)
		}
		if r.Exclude != "" && r.Pattern == "" {
			return nil, fmt.Errorf("upstream: params.json: rule %d has an exclusion and no pattern", i)
		}
		for _, f := range append(append([]string{}, r.Drop...), r.DropMessageFields...) {
			if secretShaped(f) {
				return nil, fmt.Errorf("upstream: params.json: rule %d names a credential-shaped field", i)
			}
		}
		pc := paramCompiled{ParamRule: r}
		if r.Pattern != "" {
			pc.re = compileGlob(r.Pattern)
		}
		if r.Exclude != "" {
			pc.exclude = compileGlob(r.Exclude)
		}
		c.params = append(c.params, pc)
	}
	return c, nil
}

func secretShaped(name string) bool {
	l := strings.ToLower(name)
	for _, f := range []string{"key", "secret", "token", "password", "authorization"} {
		if strings.Contains(l, f) {
			return true
		}
	}
	return false
}

func joinRegexp(name string, parts []string) (*regexp.Regexp, error) {
	if len(parts) == 0 {
		return nil, fmt.Errorf("upstream: %s has no patterns", name)
	}
	re, err := regexp.Compile("(?i)" + strings.Join(parts, "|"))
	if err != nil {
		// A regexp error quotes the pattern, which is data-file text; it is
		// reported by kind only.
		return nil, fmt.Errorf("upstream: %s: a pattern does not compile", name)
	}
	return re, nil
}

// owners lists the names a provider is known by.
func (c *Capabilities) owners(provider string) []string {
	out := []string{provider}
	if c.registry == nil {
		return out
	}
	if id, ok := c.registry.Resolve(provider); ok {
		if id != provider {
			out = append(out, id)
		}
		if e, ok := c.registry.ByID(id); ok && e.Alias != "" && e.Alias != provider && e.Alias != id {
			out = append(out, e.Alias)
		}
	}
	return out
}

func modelNames(model string) []string {
	names := []string{model}
	if i := strings.LastIndex(model, "/"); i >= 0 && i < len(model)-1 {
		names = append(names, model[i+1:])
	}
	return names
}

// For resolves what a model can do: the provider's own row, then the exact
// model, then the first matching glob, over the defaults. Only a provider row is
// returned as written; the other steps turn vision on when the name says so, and
// a name on the not-vision list never gets it.
func (c *Capabilities) For(provider, model string) Capability {
	out := c.forModel(provider, model)
	if c.registry != nil {
		if id, ok := c.registry.Resolve(provider); ok {
			if e, ok := c.registry.ByID(id); ok && e.Transport.ThinkingFormat != "" {
				out.ThinkingFormat = e.Transport.ThinkingFormat
			}
		}
	}
	return out
}

func (c *Capabilities) forModel(provider, model string) Capability {
	out := defaultCapability()
	apply := func(raw json.RawMessage) { _ = json.Unmarshal(raw, &out) }
	names := modelNames(model)
	for _, o := range c.owners(provider) {
		for _, n := range names {
			if raw, ok := c.caps.Providers[o][n]; ok {
				apply(raw)
				return out
			}
		}
	}
	matched := false
	for _, n := range names {
		if raw, ok := c.caps.Models[n]; ok {
			apply(raw)
			matched = true
			break
		}
	}
	if !matched {
		for _, r := range c.capRules {
			if r.provider != "" && !contains(c.owners(provider), r.provider) {
				continue
			}
			if anyMatch(r.re, names) {
				apply(r.set)
				break
			}
		}
	}
	if !out.Vision && c.looksLikeVision(model) {
		out.Vision = true
	}
	return out
}

func (c *Capabilities) looksLikeVision(model string) bool {
	if model == "" || c.visionNo.MatchString(model) {
		return false
	}
	return c.visionOk.MatchString(model)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func anyMatch(re *regexp.Regexp, names []string) bool {
	for _, n := range names {
		if re.MatchString(n) {
			return true
		}
	}
	return false
}

// Levels are the thinking levels a model accepts: nil when it does not reason.
// Otherwise the registry's declared levels, then the first matching override,
// then the format's set, then the base set. none goes when the model cannot
// disable thinking.
func (c *Capabilities) Levels(provider, model string, declared []string) []string {
	cp := c.For(provider, model)
	if !cp.Reasoning {
		return nil
	}
	levels := declared
	names := modelNames(model)
	if len(levels) == 0 {
		for i, r := range c.thinking.Rules {
			if r.Provider != "" && !contains(c.owners(provider), r.Provider) {
				continue
			}
			if anyMatch(c.thinkRe[i], names) {
				levels = r.Levels
				break
			}
		}
	}
	if len(levels) == 0 && cp.ThinkingFormat != "" {
		levels = c.thinking.Formats[cp.ThinkingFormat]
	}
	if len(levels) == 0 {
		levels = c.thinking.Base
	}
	out := make([]string, 0, len(levels))
	for _, l := range levels {
		if l == "none" && !cp.ThinkingCanOff {
			continue
		}
		out = append(out, l)
	}
	return out
}

// Budget is the token budget of a level, or zero for an unknown one.
func (c *Capabilities) Budget(level string) int { return c.thinking.Budgets[level] }

// ClaudeSignature and GeminiSignature are the placeholder signatures.
func (c *Capabilities) ClaudeSignature() string { return c.thinking.ClaudeSignature }
func (c *Capabilities) GeminiSignature() string { return c.thinking.GeminiSignature }

// ParamRules are the parameter rules that apply to a model, in file order.
func (c *Capabilities) ParamRules(provider, model string) []ParamRule {
	names := modelNames(model)
	owners := c.owners(provider)
	var out []ParamRule
	for _, r := range c.params {
		if r.Provider != "" && !contains(owners, r.Provider) {
			continue
		}
		if r.re != nil && !anyMatch(r.re, names) {
			continue
		}
		if r.exclude != nil && anyMatch(r.exclude, names) {
			continue
		}
		out = append(out, r.ParamRule)
	}
	return out
}
