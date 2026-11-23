// Package catalog lists the models a caller can call: the embedded list of each
// provider with an active connection, the provider's custom models and alias
// targets, minus the switched-off ones, and for a user node its own list. It
// reads stores and fetches a node's list; it never writes.
package catalog

import (
	"context"
	"slices"
	"sort"
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/routing"
	"rllmx/app/llm/upstream"
	"rllmx/app/platform/text"
	"rllmx/app/store/repository"
)

const (
	maxIDLen = 256
	kindLLM  = "llm"
)

// KV reads the key-value scopes the lists are built from.
type KV interface {
	List(ctx context.Context, scope string) (map[string]string, error)
	DisabledModels(ctx context.Context, provider string) ([]string, error)
}

// Model is one entry of a provider's list.
type Model struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	// Source is builtin, custom, alias or node.
	Source string `json:"source"`
	// Kind is the stored type of a custom model; empty means a chat model.
	Kind string `json:"-"`
}

func clean(s string) bool { return text.StripControl(s) == s }

func accepted(m Model) bool {
	return m.ID != "" && len(m.ID) <= maxIDLen && clean(m.ID) && clean(m.Name)
}

// owners are the names a provider's rows are stored under: its id, its alias
// and its extra aliases. A node's are its id and its prefix.
func owners(names ...string) []string {
	out := make([]string, 0, len(names))
	seen := map[string]bool{}
	for _, n := range names {
		if n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

func entryOwners(e upstream.Entry) []string {
	return owners(append([]string{e.ID, e.Alias}, e.Aliases...)...)
}

// RegistryModels is the embedded list plus the provider's custom models, minus
// the ones switched off. A custom model with the id of a built-in one replaces
// it rather than appearing twice.
func RegistryModels(ctx context.Context, kv KV, e upstream.Entry) ([]Model, error) {
	all, err := RegistryAll(ctx, kv, []upstream.Entry{e})
	if err != nil {
		return nil, err
	}
	return all[0], nil
}

// RegistryAll is RegistryModels over several entries, reading each kv scope
// once: the dashboard lists every provider, and a read per provider per scope
// would be hundreds of queries.
func RegistryAll(ctx context.Context, kv KV, entries []upstream.Entry) ([][]Model, error) {
	custom, err := kv.List(ctx, repository.ScopeCustomModels)
	if err != nil {
		return nil, err
	}
	disabled, err := kv.List(ctx, repository.ScopeDisabledModels)
	if err != nil {
		return nil, err
	}
	out := make([][]Model, len(entries))
	for i, e := range entries {
		names := entryOwners(e)
		off := map[string]bool{}
		for _, n := range names {
			for _, m := range repository.StoredStrings(disabled[n]) {
				off[m] = true
			}
		}
		out[i] = merge(baseModels(e), names, custom, nil, off)
	}
	return out, nil
}

func baseModels(e upstream.Entry) []Model {
	base := make([]Model, 0, len(e.Models))
	for _, m := range e.Models {
		base = append(base, Model{ID: m.ID, Name: m.Name, Source: "builtin", Kind: m.Kind})
	}
	return base
}

func disabledSet(ctx context.Context, kv KV, names []string) (map[string]bool, error) {
	off := map[string]bool{}
	for _, n := range names {
		list, err := kv.DisabledModels(ctx, n)
		if err != nil {
			return nil, err
		}
		for _, m := range list {
			off[m] = true
		}
	}
	return off, nil
}

// merge applies the rules every list shares: custom rows replace a base entry of
// the same id, alias targets only add, a switched-off id is dropped, and an id
// that is empty, long or holds a control character is dropped.
func merge(base []Model, names []string, custom map[string]string, aliases map[string]string, off map[string]bool) []Model {
	byID := map[string]Model{}
	order := []string{}
	add := func(m Model, replace bool) {
		m.Name = text.CutBytes(m.Name, maxIDLen)
		if !accepted(m) || off[m.ID] {
			return
		}
		if _, dup := byID[m.ID]; dup {
			if replace {
				byID[m.ID] = m
			}
			return
		}
		order = append(order, m.ID)
		byID[m.ID] = m
	}
	for _, m := range base {
		add(m, false)
	}
	keys := make([]string, 0, len(custom))
	for k := range custom {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, n := range names {
			rest, ok := strings.CutPrefix(k, n+"|")
			if !ok {
				continue
			}
			parts := strings.SplitN(rest, "|", 2)
			id, kind := parts[0], ""
			if len(parts) == 2 {
				kind = parts[1]
			}
			if v := gjson.Get(custom[k], "id").String(); v != "" {
				id = v
			}
			if v := gjson.Get(custom[k], "type").String(); v != "" {
				kind = v
			}
			// The dashboard stores the id as the name when the user types none,
			// so a custom row that has no description of its own keeps the
			// built-in one it replaces.
			name := gjson.Get(custom[k], "name").String()
			if name == "" || name == id {
				name = byID[id].Name
			}
			add(Model{ID: id, Name: name, Source: "custom", Kind: kind}, true)
			break
		}
	}
	targets := make([]string, 0, len(aliases))
	for k := range aliases {
		targets = append(targets, k)
	}
	sort.Strings(targets)
	for _, k := range targets {
		p, m, ok := routing.ParseAlias(aliases[k])
		if ok && slices.Contains(names, p) {
			add(Model{ID: m, Source: "alias"}, false)
		}
	}
	out := make([]Model, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	return out
}
