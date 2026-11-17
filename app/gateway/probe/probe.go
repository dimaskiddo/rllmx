// Package probe asks whether a connection's models answer. Every ping is an
// ordinary pipeline request pinned to that connection, so a failing model is
// locked, a working one is cleared, and the spend appears in usage like any
// other request. Results are cached briefly so a dashboard that asks twice does
// not spend twice.
package probe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/tidwall/gjson"
	"golang.org/x/sync/errgroup"

	"rllmx/app/access/identity"
	"rllmx/app/gateway/catalog"
	"rllmx/app/gateway/pipeline"
	"rllmx/app/llm/stream"
	"rllmx/app/llm/upstream"
	"rllmx/app/store/repository"
)

const (
	// timeout bounds one ping: a model that has not answered by then has not
	// answered.
	timeout = 10 * time.Second
	// parallel is how many pings run at once, after the warm-up.
	parallel = 3
	// probeCap is how many candidates a connection is probed for; a
	// connection that names its own models is not capped.
	probeCap = 5
	// listCap is the ceiling for an explicit test of every model.
	listCap = 50
)

// maxTokens leaves room for a model that spends its first tokens thinking.
const maxTokens = 1024

// Handler answers one request; *pipeline.Pipeline is the real one.
type Handler interface {
	Handle(ctx context.Context, in pipeline.Input, open pipeline.Opener) pipeline.Output
}

// ConnReader reads one connection.
type ConnReader interface {
	Get(ctx context.Context, id string) (repository.Connection, bool, error)
}

// NodeReader reads one provider node.
type NodeReader interface {
	Get(ctx context.Context, id string) (repository.Node, bool, error)
}

// Result is one model's outcome.
type Result struct {
	// Model is the name the pipeline was asked about, provider included.
	Model     string    `json:"model"`
	ModelID   string    `json:"modelId"`
	Name      string    `json:"name,omitempty"`
	Provider  string    `json:"provider"`
	OK        bool      `json:"ok"`
	LatencyMs int64     `json:"latencyMs"`
	Status    int       `json:"status"`
	Error     string    `json:"error,omitempty"`
	At        time.Time `json:"-"`
}

// Options are the prober's dependencies. Pipeline is a builder because the
// stores open after the routes are declared.
type Options struct {
	Pipeline func() (Handler, error)
	Nodes    func() NodeReader
	Registry *upstream.Registry
	KV       func() catalog.KV
	Now      func() time.Time
}

// Prober probes connections and models.
type Prober struct {
	o     Options
	cache *cache
}

// New builds a prober.
func New(o Options) *Prober {
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Prober{o: o, cache: newCache()}
}

// Connection probes one connection's models, skipping ones probed in the last
// few minutes unless force says otherwise. The first candidate runs alone, so a
// token refresh happens once instead of in three parallel pings.
func (p *Prober) Connection(ctx context.Context, conn repository.Connection, force bool) []Result {
	list, own, err := p.candidates(ctx, conn)
	if err != nil || len(list) == 0 {
		return nil
	}
	// A connection that names its own models is probed for all of them; the
	// provider's embedded list is only sampled.
	if !own && len(list) > probeCap {
		list = list[:probeCap]
	}
	return p.run(ctx, conn, list, force)
}

// All probes every candidate of a connection, up to listCap, for an explicit
// test. It never reads the cache: the caller asked for an answer now.
func (p *Prober) All(ctx context.Context, conn repository.Connection) ([]Result, error) {
	list, _, err := p.candidates(ctx, conn)
	if err != nil {
		return nil, err
	}
	if len(list) > listCap {
		list = list[:listCap]
	}
	return p.run(ctx, conn, list, true), nil
}

// Model probes one model name with the normal account selection: the route that
// asks about a model the caller typed has no connection to pin.
func (p *Prober) Model(ctx context.Context, model string) Result {
	return p.ping(ctx, model, "", "")
}

// Probed returns the cached probe of a connection's models, for a read route
// that ranks on it. It spends nothing.
func (p *Prober) Probed(ctx context.Context, conn repository.Connection) []Result {
	list, _, err := p.candidates(ctx, conn)
	if err != nil {
		return nil
	}
	now := p.o.Now()
	out := make([]Result, 0, len(list))
	for _, c := range list {
		if r, ok := p.cache.get(conn.ID+"|"+c.id, now); ok {
			out = append(out, r)
		}
	}
	return out
}

// Models names the models a connection would be probed for, the provider
// included, in probe order.
func (p *Prober) Models(ctx context.Context, conn repository.Connection) ([]string, error) {
	list, _, err := p.candidates(ctx, conn)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(list))
	for i, c := range list {
		out[i] = c.model
	}
	return out, nil
}

// run probes the list: the first alone, then the rest in parallel.
func (p *Prober) run(ctx context.Context, conn repository.Connection, list []candidate, force bool) []Result {
	now := p.o.Now()
	out := make([]Result, len(list))
	var pending []int
	for i, c := range list {
		if !force {
			if r, ok := p.cache.get(conn.ID+"|"+c.id, now); ok {
				out[i] = r
				continue
			}
		}
		pending = append(pending, i)
	}
	if len(pending) == 0 {
		return out
	}
	// The first ping runs alone: an expired token is renewed once, not three
	// times in parallel.
	out[pending[0]] = p.one(ctx, conn, list[pending[0]])
	pending = pending[1:]
	var g errgroup.Group
	g.SetLimit(parallel)
	for _, i := range pending {
		g.Go(func() error {
			out[i] = p.one(ctx, conn, list[i])
			return nil
		})
	}
	_ = g.Wait()
	return out
}

func (p *Prober) one(ctx context.Context, conn repository.Connection, c candidate) Result {
	r := p.ping(ctx, c.model, conn.ID, c.id)
	r.Provider = conn.Provider
	r.At = p.o.Now()
	p.cache.put(conn.ID+"|"+c.id, r, p.o.Now())
	return r
}

// ping sends the smallest chat request and reads the outcome.
func (p *Prober) ping(ctx context.Context, model, connID, modelID string) Result {
	r := Result{Model: model, ModelID: modelID}
	if modelID == "" {
		r.ModelID = model
	}
	h, err := p.handler()
	if err != nil {
		r.Error = "The pipeline is not ready"
		return r
	}
	body, err := requestBody(model)
	if err != nil {
		r.Error = "The probe request cannot be built"
		return r
	}
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := p.o.Now()
	out := h.Handle(pctx, pipeline.Input{
		Path: "/v1/chat/completions", Body: body, Caller: identity.Probe(),
		RequestID: "probe-" + fmt.Sprint(start.UnixNano()), Connection: connID, Cache: "bypass",
	}, func() (stream.Client, error) { return nil, errors.New("a probe never streams") })
	r.LatencyMs = p.o.Now().Sub(start).Milliseconds()
	r.Status = out.Status
	r.OK = out.Status >= 200 && out.Status <= 299
	if !r.OK {
		r.Error = errorText(out.Body, out.Status)
	}
	return r
}

func (p *Prober) handler() (Handler, error) {
	if p.o.Pipeline == nil {
		return nil, errors.New("probe: no pipeline")
	}
	return p.o.Pipeline()
}

func requestBody(model string) ([]byte, error) {
	return json.Marshal(struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		MaxTokens int  `json:"max_tokens"`
		Stream    bool `json:"stream"`
	}{Model: model, Messages: []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}{{Role: "user", Content: "hi"}}, MaxTokens: maxTokens})
}

// errorText is the client-facing message, never the body.
func errorText(body []byte, status int) string {
	if msg := gjson.GetBytes(body, "error.message").String(); msg != "" {
		return msg
	}
	if text := http.StatusText(status); text != "" {
		return "The model did not answer: " + text
	}
	return "The model did not answer"
}

// candidate is one model to probe, with the spelling that reaches it.
type candidate struct {
	id    string
	model string
}

// enabledModels is the connection's providerSpecificData.enabledModels list.
func enabledModels(c repository.Connection) []string {
	raw, ok := c.Doc().GetRaw("providerSpecificData")
	if !ok {
		return nil
	}
	var out []string
	for _, v := range gjson.Get(raw, "enabledModels").Array() {
		if v.Type == gjson.String && v.String() != "" {
			out = append(out, v.String())
		}
	}
	return out
}

// candidates is a connection's models in the order they are probed: the
// default first, then the connection's own list, then the provider's list with
// the disabled ones already gone. own says the list came from the connection.
func (p *Prober) candidates(ctx context.Context, conn repository.Connection) (out []candidate, own bool, err error) {
	prefix, models, own, err := p.list(ctx, conn)
	if err != nil {
		return nil, false, err
	}
	defaultModel, _ := conn.Doc().GetString("defaultModel")
	out = make([]candidate, 0, len(models)+1)
	seen := map[string]bool{}
	add := func(id string) {
		if id == "" || seen[id] || prefix == "" {
			return
		}
		seen[id] = true
		out = append(out, candidate{id: id, model: prefix + "/" + id})
	}
	add(defaultModel)
	for _, m := range models {
		add(m)
	}
	return out, own, nil
}

// list names the prefix a connection's models are reached through, the model
// ids themselves, and whether the connection named them.
func (p *Prober) list(ctx context.Context, conn repository.Connection) (string, []string, bool, error) {
	if upstream.NodeKind(conn.Provider) != "" {
		node, found, err := p.o.Nodes().Get(ctx, conn.Provider)
		if err != nil || !found {
			return "", nil, false, err
		}
		// A node answers under its prefix; a node without one cannot be
		// addressed at all.
		prefix, _ := node.Doc().GetString("prefix")
		if own := ownModels(conn); len(own) > 0 {
			return prefix, own, true, nil
		}
		return prefix, nil, false, nil
	}
	if own := ownModels(conn); len(own) > 0 {
		return conn.Provider, own, true, nil
	}
	entry, found := p.o.Registry.ByID(conn.Provider)
	if !found {
		return "", nil, false, errors.New("probe: unknown provider")
	}
	list, err := catalog.RegistryModels(ctx, p.o.KV(), entry)
	if err != nil {
		return "", nil, false, err
	}
	ids := make([]string, 0, len(list))
	for _, m := range list {
		if m.Kind != "" && m.Kind != upstream.KindLLM {
			continue
		}
		ids = append(ids, m.ID)
	}
	return conn.Provider, ids, false, nil
}

// ownModels is the model list the connection itself carries: its stored
// models array, else the enabled ones. A row imported from the reference can
// carry either.
func ownModels(c repository.Connection) []string {
	doc := c.Doc()
	if raw, ok := doc.GetRaw("models"); ok {
		var out []string
		for _, v := range gjson.Parse(raw).Array() {
			if v.Type == gjson.String && v.String() != "" {
				out = append(out, v.String())
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return enabledModels(c)
}

// cache is the short memory of probe results, bounded like the sticky store.
const (
	cacheLife  = 5 * time.Minute
	cacheLimit = 2000
)

type cached struct {
	result Result
	at     time.Time
}

type cache struct {
	mu    sync.Mutex
	items map[string]cached
}

func newCache() *cache { return &cache{items: map[string]cached{}} }

func (c *cache) get(key string, now time.Time) (Result, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[key]
	if !ok || now.Sub(e.at) > cacheLife {
		return Result{}, false
	}
	return e.result, true
}

func (c *cache) put(key string, r Result, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[key] = cached{result: r, at: now}
	if len(c.items) <= cacheLimit {
		return
	}
	oldestKey := ""
	var oldestAt time.Time
	for k, e := range c.items {
		if k == key {
			continue
		}
		if oldestKey == "" || e.at.Before(oldestAt) {
			oldestKey, oldestAt = k, e.at
		}
	}
	if oldestKey != "" {
		delete(c.items, oldestKey)
	}
}
