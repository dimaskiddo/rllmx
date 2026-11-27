package upstream

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
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
	// VisionSet is true when a table, the catalog or an operator said something
	// about vision. A model we know nothing about is tried with an image rather
	// than stripped, so this is what tells the two apart.
	VisionSet bool `json:"-"`
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
	// LongDecode marks a model that returns at most MaxOutput tokens per call but
	// continues on a token, so a request may ask for up to LongDecodeCeiling.
	LongDecode bool `json:"longDecode,omitempty"`
}

// LongDecodeCeiling is the most output tokens a long decode may produce in all.
const LongDecodeCeiling = 1 << 20

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
	// GeminiCLISignature is the placeholder for the Gemini CLI upstream.
	//
	// AntigravitySignature and VertexSignature are the placeholders for the rest of the
	// placeholder thought signatures each Gemini-family upstream accepts on a
	// function call that has none.
	GeminiCLISignature   string `json:"geminiCliSignature"`
	AntigravitySignature string `json:"antigravitySignature"`
	VertexSignature      string `json:"vertexSignature"`
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
	// overlay is the synced catalog, swapped whole; nil means the tables alone.
	overlay atomic.Pointer[Overlay]
	// custom is what an operator declared for a custom model, keyed
	// "provider/model" and swapped whole. declareMu serializes the swap with the
	// read-modify-write a learned capability does.
	custom    atomic.Pointer[map[string]Declared]
	declareMu sync.Mutex
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
	if c.thinking.GeminiCLISignature == "" || c.thinking.AntigravitySignature == "" || c.thinking.VertexSignature == "" {
		return nil, errors.New("upstream: thinking.json: the Gemini CLI, Antigravity and Vertex signatures are required")
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
			if !safeToDrop(f) {
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

// dropSafeFields are names that read like a credential but are a cache hint, so
// a rule may drop them. Every other credential-shaped name stays refused.
var dropSafeFields = map[string]bool{"prompt_cache_key": true}

// safeToDrop reports whether a rule may name the field, which it may not when the
// name reads like a credential.
func safeToDrop(name string) bool {
	return dropSafeFields[name] || !secretShaped(name)
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

// Declared is what an operator said about one custom model. A nil flag means
// the operator said nothing and the resolved table stands; a set flag wins, so
// unticking Vision turns it off.
type Declared struct {
	Vision    *bool
	Reasoning *bool
	Audio     *bool
}

// For resolves what a model can do: the provider's own row, then the exact
// model, then the first matching glob, over the defaults, then the synced
// catalog. Only a provider row is returned as written; the other steps turn
// vision on when the name says so, and a name on the not-vision list never gets
// it. An operator's declaration then replaces the flags it names.
func (c *Capabilities) For(provider, model string) Capability {
	out, providerRow := c.table(provider, model)
	if !providerRow {
		c.refine(&out, provider, model)
		c.finish(&out, model)
	}
	if d, ok := c.declared(provider, model); ok {
		if d.Vision != nil {
			out.Vision, out.VisionSet = *d.Vision, true
		}
		if d.Reasoning != nil {
			out.Reasoning = *d.Reasoning
		}
		if d.Audio != nil {
			out.AudioInput = *d.Audio
		}
	}
	if c.registry != nil {
		if id, ok := c.registry.Resolve(provider); ok {
			if e, ok := c.registry.ByID(id); ok && e.Transport.ThinkingFormat != "" {
				out.ThinkingFormat = e.Transport.ThinkingFormat
			}
		}
	}
	return out
}

// SetCustom replaces the capabilities an operator declared for custom models.
func (c *Capabilities) SetCustom(all map[string]Declared) {
	c.declareMu.Lock()
	defer c.declareMu.Unlock()
	c.custom.Store(&all)
}

// Declare records one capability a request proved a custom model has, keeping
// every other entry as it is.
func (c *Capabilities) Declare(provider, model string, d Declared) {
	c.declareMu.Lock()
	defer c.declareMu.Unlock()
	old := c.custom.Load()
	next := make(map[string]Declared, 1)
	if old != nil {
		for k, v := range *old {
			next[k] = v
		}
	}
	key := provider + "/" + model
	d.Vision, d.Reasoning = mergeFlag(next[key].Vision, d.Vision), mergeFlag(next[key].Reasoning, d.Reasoning)
	d.Audio = mergeFlag(next[key].Audio, d.Audio)
	next[key] = d
	c.custom.Store(&next)
}

// mergeFlag keeps the old declaration for a flag the new one does not name.
func mergeFlag(old, next *bool) *bool {
	if next != nil {
		return next
	}
	return old
}

// declared is what an operator said about one custom model, under any name the
// provider is known by: the dashboard stores the alias, and a request resolves
// to the registry id.
func (c *Capabilities) declared(provider, model string) (Declared, bool) {
	all := c.custom.Load()
	if all == nil {
		return Declared{}, false
	}
	for _, o := range c.owners(provider) {
		if d, ok := (*all)[o+"/"+model]; ok {
			return d, true
		}
	}
	return Declared{}, false
}

// Base resolves without the synced catalog: what the tables say on their own.
// A sync compares against it, so the catalog never sees its own last answer.
func (c *Capabilities) Base(provider, model string) Capability {
	out, providerRow := c.table(provider, model)
	if !providerRow {
		c.finish(&out, model)
	}
	return out
}

func (c *Capabilities) finish(out *Capability, model string) {
	if !out.Vision && c.looksLikeVision(model) {
		out.Vision, out.VisionSet = true, true
	}
}

// refine lays the synced catalog over a table result: a declared modality only
// turns a capability on, and a positive limit replaces the table's number.
func (c *Capabilities) refine(out *Capability, provider, model string) {
	ov := c.overlay.Load()
	if ov == nil {
		return
	}
	id := baseID(model)
	limitsDone := false
	for _, o := range c.owners(provider) {
		if m, ok := ov.Models[o+":"+id]; ok {
			out.Vision, out.VisionSet = out.Vision || m.Vision, true
			out.PDF = out.PDF || m.PDF
			out.AudioInput = out.AudioInput || m.AudioInput
			out.VideoInput = out.VideoInput || m.VideoInput
		}
		if limitsDone {
			continue
		}
		if l, ok := ov.Limits(o, model); ok {
			limitsDone = true
			if l.ContextWindow > 0 {
				out.ContextWindow = l.ContextWindow
			}
			if l.MaxOutput > 0 {
				out.MaxOutput = l.MaxOutput
			}
		}
	}
}

// table resolves the embedded tables alone and reports whether a provider's own
// row answered, which the catalog may not overwrite.
func (c *Capabilities) table(provider, model string) (Capability, bool) {
	out := defaultCapability()
	apply := func(raw json.RawMessage) { _ = json.Unmarshal(raw, &out) }
	names := modelNames(model)
	for _, o := range c.owners(provider) {
		for _, n := range names {
			if raw, ok := c.caps.Providers[o][n]; ok {
				apply(raw)
				out.VisionSet = true
				return out, true
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
			if r.provider != "" && !slices.Contains(c.owners(provider), r.provider) {
				continue
			}
			if anyMatch(r.re, names) {
				apply(r.set)
				matched = true
				break
			}
		}
	}
	out.VisionSet = matched
	return out, false
}

func (c *Capabilities) looksLikeVision(model string) bool {
	if model == "" || c.visionNo.MatchString(model) {
		return false
	}
	return c.visionOk.MatchString(model)
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
			if r.Provider != "" && !slices.Contains(c.owners(provider), r.Provider) {
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

// GeminiCLISignature is the placeholder for the Gemini CLI upstream;
// AntigravitySignature and VertexSignature are the ones for the other two
// Google upstreams.
func (c *Capabilities) GeminiCLISignature() string   { return c.thinking.GeminiCLISignature }
func (c *Capabilities) AntigravitySignature() string { return c.thinking.AntigravitySignature }
func (c *Capabilities) VertexSignature() string      { return c.thinking.VertexSignature }

// ParamRules are the parameter rules that apply to a model, in file order.
func (c *Capabilities) ParamRules(provider, model string) []ParamRule {
	names := modelNames(model)
	owners := c.owners(provider)
	var out []ParamRule
	for _, r := range c.params {
		if r.Provider != "" && !slices.Contains(owners, r.Provider) {
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
