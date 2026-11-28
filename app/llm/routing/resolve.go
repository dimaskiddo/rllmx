package routing

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"rllmx/app/llm/upstream"
	"rllmx/app/platform/text"
)

const maxInput = 256

var (
	// ErrEmpty means nothing was left once the suffixes were removed.
	ErrEmpty = errors.New("routing: empty model")
	// ErrTooLong means the input is over 256 bytes.
	ErrTooLong = errors.New("routing: model name is too long")
	// ErrBadInput means the input holds a control character.
	ErrBadInput = errors.New("routing: model name has a control character")
)

// builtinAliases are model names this program maps itself.
var builtinAliases = map[string]string{"grok-build": "gcli/grok-build"}

// nodeTypes are tried in this order when a prefix names a user node.
var nodeTypes = []string{"openai-compatible", "anthropic-compatible", "custom-embedding"}

// Target is where a model string leads.
type Target struct {
	Provider string
	Model    string
	// Combo is set, and Provider left empty, when the name is a combo.
	Combo         string
	ContextMarker bool
	Thinking      Thinking
}

// Resolver resolves model strings. It does not decide access.
type Resolver struct {
	Registry *upstream.Registry
	Snapshot *Snapshot
}

// Resolve turns what a client sent into a Target.
func (r *Resolver) Resolve(ctx context.Context, input string) (Target, error) {
	if len(input) > maxInput {
		return Target{}, ErrTooLong
	}
	if text.HasControl(input) {
		return Target{}, ErrBadInput
	}
	name, marker, thinking := splitSuffixes(input)
	if name == "" {
		return Target{}, ErrEmpty
	}
	t := Target{ContextMarker: marker, Thinking: thinking}

	if prefix, model, ok := strings.Cut(name, "/"); ok {
		if model == "" || prefix == "" {
			return Target{}, ErrEmpty
		}
		provider, err := r.providerFor(ctx, prefix)
		if err != nil {
			return Target{}, err
		}
		t.Provider, t.Model = provider, model
		return t, nil
	}

	combos, err := r.Snapshot.ComboNames(ctx)
	if err != nil {
		return Target{}, err
	}
	if slices.Contains(combos, name) {
		t.Combo, t.Model = name, name
		return t, nil
	}
	aliases, err := r.Snapshot.Aliases(ctx)
	if err != nil {
		return Target{}, err
	}
	if p, m, ok := r.aliasTarget(aliases[name]); ok {
		provider, err := r.claudeOAuth(ctx, p)
		if err != nil {
			return Target{}, err
		}
		t.Provider, t.Model = provider, m
		return t, nil
	}
	if p, m, ok := r.aliasTarget(`"` + builtinAliases[name] + `"`); ok {
		provider, err := r.claudeOAuth(ctx, p)
		if err != nil {
			return Target{}, err
		}
		t.Provider, t.Model = provider, m
		return t, nil
	}
	provider, err := r.claudeOAuth(ctx, infer(name))
	if err != nil {
		return Target{}, err
	}
	t.Provider, t.Model = provider, name
	return t, nil
}

// claudeOAuth offers a bare Claude model to the OAuth provider when the
// API-key provider has no active connection and the OAuth one does, which is
// where the reference sends a `claude-*` id. Any other provider, and a missing
// answer on either side, leaves the provider as it was.
func (r *Resolver) claudeOAuth(ctx context.Context, provider string) (string, error) {
	if provider != "anthropic" {
		return provider, nil
	}
	active, err := r.Snapshot.ActiveProviders(ctx)
	if err != nil {
		return "", err
	}
	if slices.Contains(active, "anthropic") || !slices.Contains(active, "claude") {
		return provider, nil
	}
	return "claude", nil
}

// providerFor maps the prefix of `prefix/model`. A built-in name always wins:
// only a prefix that is not one is matched against user nodes.
func (r *Resolver) providerFor(ctx context.Context, prefix string) (string, error) {
	if r.Registry.Reserved(prefix) {
		id, _ := r.Registry.Resolve(prefix)
		return id, nil
	}
	nodes, err := r.Snapshot.Nodes(ctx)
	if err != nil {
		return "", err
	}
	for _, typ := range nodeTypes {
		for _, n := range nodes {
			if n.Type == typ && n.Prefix != "" && n.Prefix == prefix {
				return n.ID, nil
			}
		}
	}
	return prefix, nil
}

// aliasTarget reads a stored alias and maps its provider token to a registry id
// when it is one. The result is returned as it is and never looked up as an
// alias again, so aliases cannot chain or loop.
func (r *Resolver) aliasTarget(raw string) (provider, model string, ok bool) {
	provider, model, ok = ParseAlias(raw)
	if !ok {
		return "", "", false
	}
	if id, found := r.Registry.Resolve(provider); found {
		provider = id
	}
	return provider, model, true
}

// ParseAlias reads a stored alias value: the text "provider/model" or an object
// with both fields. Any other shape is not an alias.
func ParseAlias(raw string) (provider, model string, ok bool) {
	if raw == "" {
		return "", "", false
	}
	var v any
	if json.Unmarshal([]byte(raw), &v) != nil {
		return "", "", false
	}
	switch x := v.(type) {
	case string:
		p, m, cut := strings.Cut(x, "/")
		if !cut || p == "" || m == "" {
			return "", "", false
		}
		return p, m, true
	case map[string]any:
		p, _ := x["provider"].(string)
		m, _ := x["model"].(string)
		if p == "" || m == "" {
			return "", "", false
		}
		return p, m, true
	}
	return "", "", false
}
