package upstream

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
)

//go:embed data/pricing/prices.json
var embeddedPrices []byte

// maxPriceName is the longest model name that gets a price.
const maxPriceName = 256

// Rates are dollars per million tokens. Cached and CacheCreation fall back to
// Input and Reasoning to Output when the data leaves them out.
type Rates struct {
	Input         float64
	Output        float64
	Cached        float64
	Reasoning     float64
	CacheCreation float64
}

// Usage is the token counts of one request. Prompt includes the cached and
// cache-creation tokens, Completion includes the reasoning tokens.
type Usage struct {
	Prompt        int64
	Completion    int64
	Cached        int64
	CacheCreation int64
	Reasoning     int64
}

// Cost is the price of u in dollars. A share larger than the total it belongs
// to is cut back to it, and a negative count is zero.
func (r Rates) Cost(u Usage) float64 {
	prompt := max(u.Prompt, 0)
	cached := min(max(u.Cached, 0), prompt)
	creation := min(max(u.CacheCreation, 0), prompt-cached)
	completion := max(u.Completion, 0)
	reasoning := min(max(u.Reasoning, 0), completion)
	total := float64(prompt-cached-creation)*r.Input +
		float64(cached)*r.Cached +
		float64(creation)*r.CacheCreation +
		float64(completion-reasoning)*r.Output +
		float64(reasoning)*r.Reasoning
	return total / 1e6
}

type rawRates struct {
	Input         *float64 `json:"input"`
	Output        *float64 `json:"output"`
	Cached        *float64 `json:"cached"`
	Reasoning     *float64 `json:"reasoning"`
	CacheCreation *float64 `json:"cacheCreation"`
}

func (w rawRates) rates() (Rates, error) {
	val := func(p *float64) (float64, error) {
		if p == nil {
			return 0, nil
		}
		if *p < 0 || math.IsNaN(*p) || math.IsInf(*p, 0) {
			return 0, errors.New("a rate is negative or not finite")
		}
		return *p, nil
	}
	in, err := val(w.Input)
	if err != nil {
		return Rates{}, err
	}
	out, err := val(w.Output)
	if err != nil {
		return Rates{}, err
	}
	r := Rates{Input: in, Output: out, Cached: in, Reasoning: out, CacheCreation: in}
	for _, f := range []struct {
		src *float64
		dst *float64
	}{{w.Cached, &r.Cached}, {w.Reasoning, &r.Reasoning}, {w.CacheCreation, &r.CacheCreation}} {
		if f.src == nil {
			continue
		}
		if *f.dst, err = val(f.src); err != nil {
			return Rates{}, err
		}
	}
	return r, nil
}

type pricesFile struct {
	Free      []string                       `json:"free"`
	Models    map[string]rawRates            `json:"models"`
	Providers map[string]map[string]rawRates `json:"providers"`
	Patterns  []struct {
		Pattern string   `json:"pattern"`
		Rates   rawRates `json:"rates"`
	} `json:"patterns"`
}

type pattern struct {
	re    *regexp.Regexp
	rates Rates
}

// Overrides are the administrator's rate cards, keyed by provider then model.
type Overrides map[string]map[string]Rates

// ParseOverrides reads the values of the kv scope "pricing": a provider name
// to a JSON object of model to rate card. A malformed entry is skipped with a
// warning that names the provider and model and never a value.
func ParseOverrides(raw map[string]string, warn func(format string, args ...any)) Overrides {
	out := Overrides{}
	for provider, text := range raw {
		var models map[string]json.RawMessage
		if err := json.Unmarshal([]byte(text), &models); err != nil {
			warn("pricing override for %q skipped: not a JSON object", provider)
			continue
		}
		cards := map[string]Rates{}
		for model, body := range models {
			dec := json.NewDecoder(bytes.NewReader(body))
			dec.DisallowUnknownFields()
			var w rawRates
			if err := dec.Decode(&w); err != nil {
				warn("pricing override for %q model %q skipped: malformed rate card", provider, model)
				continue
			}
			r, err := w.rates()
			if err != nil {
				warn("pricing override for %q model %q skipped: %v", provider, model, err)
				continue
			}
			cards[model] = r
		}
		out[provider] = cards
	}
	return out
}

// Table looks up what a model costs. It is read-only; WithOverrides returns a
// copy that shares the baseline.
type Table struct {
	free      []string
	models    map[string]Rates
	providers map[string]map[string]Rates
	patterns  []pattern
	overrides Overrides
	registry  *Registry
}

// DefaultTable is the embedded baseline. The registry, which may be nil, lets
// a provider be tried under its id and its alias.
func DefaultTable(reg *Registry) (*Table, error) { return NewTable(embeddedPrices, reg) }

// NewTable builds a table from a prices file.
func NewTable(data []byte, reg *Registry) (*Table, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var f pricesFile
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("upstream: prices: %s", decodeReason(err))
	}
	t := &Table{models: map[string]Rates{}, providers: map[string]map[string]Rates{}, registry: reg}
	for _, ns := range f.Free {
		t.free = append(t.free, strings.ToLower(ns))
	}
	for model, w := range f.Models {
		r, err := w.rates()
		if err != nil {
			return nil, fmt.Errorf("upstream: prices: model %q: %w", model, err)
		}
		t.models[model] = r
	}
	for provider, models := range f.Providers {
		cards := map[string]Rates{}
		for model, w := range models {
			r, err := w.rates()
			if err != nil {
				return nil, fmt.Errorf("upstream: prices: provider %q model %q: %w", provider, model, err)
			}
			cards[model] = r
		}
		t.providers[provider] = cards
	}
	for _, p := range f.Patterns {
		r, err := p.Rates.rates()
		if err != nil {
			return nil, fmt.Errorf("upstream: prices: pattern %q: %w", p.Pattern, err)
		}
		t.patterns = append(t.patterns, pattern{re: compileGlob(p.Pattern), rates: r})
	}
	return t, nil
}

// compileGlob turns a pattern where only "*" is special into a case-insensitive
// regular expression. Patterns come from the embedded file, never from a user.
func compileGlob(glob string) *regexp.Regexp {
	parts := strings.Split(glob, "*")
	for i, p := range parts {
		parts[i] = regexp.QuoteMeta(p)
	}
	return regexp.MustCompile("(?i)^" + strings.Join(parts, ".*") + "$")
}

// WithOverrides returns a table that checks o before anything embedded.
func (t *Table) WithOverrides(o Overrides) *Table {
	c := *t
	c.overrides = o
	return &c
}

// Lookup finds the rates for a model as a provider serves it. The order is the
// administrator override, the embedded price of that provider, the free
// namespaces, the canonical model price and the glob patterns. A name over 256
// bytes, or an empty one, has no price.
func (t *Table) Lookup(provider, model string) (Rates, bool) {
	if model == "" || len(model) > maxPriceName {
		return Rates{}, false
	}
	names := []string{model}
	if i := strings.LastIndex(model, "/"); i >= 0 && i < len(model)-1 {
		names = append(names, model[i+1:])
	}
	owners := t.owners(provider)

	for _, o := range owners {
		for _, n := range names {
			if r, ok := t.overrides[o][n]; ok {
				return r, true
			}
		}
	}
	for _, o := range owners {
		for _, n := range names {
			if r, ok := t.providers[o][n]; ok {
				return r, true
			}
		}
	}
	lower := strings.ToLower(model)
	for _, ns := range t.free {
		if strings.HasPrefix(lower, ns) {
			return Rates{}, true
		}
	}
	for _, n := range names {
		if r, ok := t.models[n]; ok {
			return r, true
		}
	}
	for _, p := range t.patterns {
		if slices.ContainsFunc(names, p.re.MatchString) {
			return p.rates, true
		}
	}
	return Rates{}, false
}

// owners lists the names a provider is known by: as given, its id and its alias.
func (t *Table) owners(provider string) []string {
	if provider == "" {
		return nil
	}
	out := []string{provider}
	if t.registry == nil {
		return out
	}
	id, ok := t.registry.Resolve(provider)
	if !ok {
		return out
	}
	add := func(s string) {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	add(id)
	if e, ok := t.registry.ByID(id); ok && e.Alias != "" {
		add(e.Alias)
	}
	return out
}

// Cost prices a request. known is false when the model has no rates, which is
// different from a free model: the caller must not report an unknown cost as 0.
func (t *Table) Cost(provider, model string, u Usage) (cost float64, known bool) {
	r, ok := t.Lookup(provider, model)
	if !ok {
		return 0, false
	}
	return r.Cost(u), true
}
