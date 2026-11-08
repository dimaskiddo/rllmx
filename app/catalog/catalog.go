package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/goravel/framework/contracts/log"
	"github.com/tidwall/gjson"
	"golang.org/x/sync/errgroup"

	"rllmx/app/account"
	"rllmx/app/cache"
	"rllmx/app/identity"
	"rllmx/app/repository"
	"rllmx/app/routing"
	"rllmx/app/upstream"
)

const (
	nodeFetchTimeout = 5 * time.Second
	nodeCacheTTL     = 30 * time.Second
	nodeFetchLimit   = 4
)

// Connections lists provider accounts.
type Connections interface {
	List(ctx context.Context, f repository.ConnFilter) ([]repository.Connection, error)
}

// Nodes lists user-defined provider nodes.
type Nodes interface {
	List(ctx context.Context, typ string) ([]repository.Node, error)
}

// Resolver turns a model string into a provider and model.
type Resolver interface {
	Resolve(ctx context.Context, input string) (routing.Target, error)
}

// Loader is the cache's read-through load.
type Loader interface {
	Load(ctx context.Context, g cache.Group, key string, ttl time.Duration, load func(context.Context) ([]byte, error)) ([]byte, error)
}

// NodeFetch asks a node for its own model list. A node type, an address and the
// saved key are all it gets.
type NodeFetch func(ctx context.Context, nodeType, baseURL, key string) ([]Model, error)

// Catalog assembles the lists. It is safe for concurrent use.
type Catalog struct {
	Registry *upstream.Registry
	Caps     *upstream.Capabilities
	Resolver Resolver
	Conns    Connections
	Nodes    Nodes
	KV       KV
	// Fetch and Cache are optional: without Fetch a node shows only the models
	// it was given; without Cache every request fetches.
	Fetch NodeFetch
	Cache Loader
	Log   log.Log
	// Created is the time every entry reports.
	Created int64
}

// List returns the models the caller can call, in provider order.
func (c *Catalog) List(ctx context.Context, caller identity.Caller) ([]Entry, error) {
	active := true
	conns, err := c.Conns.List(ctx, repository.ConnFilter{IsActive: &active})
	if err != nil {
		return nil, err
	}
	byProvider := map[string][]repository.Connection{}
	for _, cn := range conns {
		if cn.Corrupt || account.Expired(cn) {
			continue
		}
		byProvider[cn.Provider] = append(byProvider[cn.Provider], cn)
	}
	if len(byProvider) == 0 {
		return []Entry{}, nil
	}
	custom, err := c.KV.List(ctx, repository.ScopeCustomModels)
	if err != nil {
		return nil, err
	}
	aliases, err := c.KV.List(ctx, repository.ScopeModelAliases)
	if err != nil {
		return nil, err
	}

	var out []Entry
	for _, e := range c.Registry.All() {
		cs := byProvider[e.ID]
		if len(cs) == 0 {
			continue
		}
		names := entryOwners(e)
		off, err := disabledSet(ctx, c.KV, names)
		if err != nil {
			return nil, err
		}
		base := make([]Model, 0, len(e.Models))
		for _, m := range e.Models {
			base = append(base, Model{ID: m.ID, Name: m.Name, Source: "builtin"})
		}
		owner := e.Alias
		if owner == "" {
			owner = e.ID
		}
		out = append(out, c.entries(e.ID, owner, merge(base, names, custom, aliases, off), cs)...)
	}

	nodes, err := c.Nodes.List(ctx, "")
	if err != nil {
		return nil, err
	}
	lists, err := c.nodeLists(ctx, nodes, byProvider)
	if err != nil {
		return nil, err
	}
	for i, n := range nodes {
		cs := byProvider[n.ID]
		if len(cs) == 0 || n.Type == upstream.NodeTypeEmbedding {
			continue
		}
		prefix, _ := n.Doc().GetString("prefix")
		names := owners(n.ID, prefix)
		off, err := disabledSet(ctx, c.KV, names)
		if err != nil {
			return nil, err
		}
		owner := prefix
		if owner == "" {
			owner = n.ID
		}
		out = append(out, c.entries(n.ID, owner, merge(lists[i], names, custom, aliases, off), cs)...)
	}
	return c.visible(ctx, caller, dedupe(out))
}

// Get finds one callable model by the id List reports.
func (c *Catalog) Get(ctx context.Context, caller identity.Caller, id string) (Entry, bool, error) {
	list, err := c.List(ctx, caller)
	if err != nil {
		return Entry{}, false, err
	}
	for _, e := range list {
		if e.ID == id {
			return e, true, nil
		}
	}
	return Entry{}, false, nil
}

// entries turns a merged list into entries: chat models only, and only those at
// least one of the provider's connections accepts.
func (c *Catalog) entries(provider, owner string, models []Model, conns []repository.Connection) []Entry {
	out := make([]Entry, 0, len(models))
	for _, m := range models {
		if m.Kind != "" && m.Kind != kindLLM {
			continue
		}
		if !slices.ContainsFunc(conns, func(cn repository.Connection) bool { return account.ModelEnabled(cn, m.ID) }) {
			continue
		}
		out = append(out, Entry{
			ID: owner + "/" + m.ID, Provider: provider, Model: m.ID, Name: m.Name, OwnedBy: owner, Caps: c.Caps.For(provider, m.ID),
		})
	}
	return out
}

func dedupe(list []Entry) []Entry {
	seen := make(map[string]bool, len(list))
	out := list[:0]
	for _, e := range list {
		if !seen[e.ID] {
			seen[e.ID] = true
			out = append(out, e)
		}
	}
	return out
}

// nodeLists gives, for each node, the models it was given or reports. Nodes
// with an enabled list use it; the others are asked, a few at a time.
func (c *Catalog) nodeLists(ctx context.Context, nodes []repository.Node, byProvider map[string][]repository.Connection) ([][]Model, error) {
	out := make([][]Model, len(nodes))
	var g errgroup.Group
	g.SetLimit(nodeFetchLimit)
	for i, n := range nodes {
		cs := byProvider[n.ID]
		if len(cs) == 0 || n.Type == upstream.NodeTypeEmbedding {
			continue
		}
		if enabled := enabledModels(cs); len(enabled) > 0 {
			out[i] = enabled
			continue
		}
		if c.Fetch == nil {
			continue
		}
		g.Go(func() error {
			list, err := c.fetch(ctx, n, cs)
			out[i] = list
			return err
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return out, nil
}

// enabledModels is the union of the models the node's connections enable.
func enabledModels(conns []repository.Connection) []Model {
	var out []Model
	seen := map[string]bool{}
	for _, cn := range conns {
		raw, ok := cn.Doc().GetRaw("providerSpecificData")
		if !ok {
			continue
		}
		for _, v := range gjson.Get(raw, "enabledModels").Array() {
			if id := v.String(); v.Type == gjson.String && !seen[id] {
				seen[id] = true
				out = append(out, Model{ID: id, Source: "node"})
			}
		}
	}
	return out
}

// fetch asks one node, through the cache. A node that fails or answers nothing
// usable contributes an empty list for the same window, so a down node costs
// one wait per window and never fails the list. Only a leaving caller does.
func (c *Catalog) fetch(ctx context.Context, n repository.Node, conns []repository.Connection) ([]Model, error) {
	base, _ := n.Doc().GetString("baseUrl")
	if strings.TrimSpace(base) == "" {
		return nil, nil
	}
	key := ""
	for _, cn := range conns {
		if k, ok := cn.Doc().GetString("apiKey"); ok && k != "" {
			key = k
			break
		}
	}
	load := func(lctx context.Context) ([]byte, error) {
		lctx, cancel := context.WithTimeout(lctx, nodeFetchTimeout)
		defer cancel()
		list, err := c.Fetch(lctx, n.Type, base, key)
		if err != nil {
			if c.Log != nil {
				c.Log.Debugf("catalog: node %s did not list its models", n.ID)
			}
			list = nil
		}
		for i := range list {
			list[i].Source = "node"
		}
		return json.Marshal(list)
	}
	var raw []byte
	var err error
	if c.Cache == nil {
		raw, err = load(ctx)
	} else {
		sum := sha256.Sum256([]byte(n.ID + "\x00" + base))
		raw, err = c.Cache.Load(ctx, cache.Models, "node:"+hex.EncodeToString(sum[:]), nodeCacheTTL, load)
	}
	if err != nil {
		return nil, err
	}
	var list []Model
	if json.Unmarshal(raw, &list) != nil {
		return nil, nil
	}
	return list, nil
}

// visible keeps what the caller may call. A key that is not restricted sees
// everything; a restricted one sees the listed models, each written in any form
// the resolver understands. The comparison is exact, as the resolver's is, so a
// model is never shown under a spelling the pipeline would route elsewhere.
func (c *Catalog) visible(ctx context.Context, caller identity.Caller, list []Entry) ([]Entry, error) {
	if !caller.Restricted {
		return list, nil
	}
	allow := map[string]bool{}
	for _, a := range caller.Allow {
		t, err := c.Resolver.Resolve(ctx, a)
		switch {
		case err == nil && t.Combo == "":
			allow[t.Provider+"/"+t.Model] = true
		case err == nil, errors.Is(err, routing.ErrEmpty), errors.Is(err, routing.ErrTooLong), errors.Is(err, routing.ErrBadInput):
		default:
			return nil, err
		}
	}
	out := make([]Entry, 0, len(list))
	for _, e := range list {
		if allow[e.Provider+"/"+e.Model] {
			out = append(out, e)
		}
	}
	return out, nil
}
