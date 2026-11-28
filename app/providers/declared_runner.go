package providers

import (
	"context"
	"sync"
	"time"

	"github.com/goravel/framework/contracts/foundation"
	"github.com/goravel/framework/contracts/log"
	"github.com/tidwall/sjson"

	"rllmx/app/gateway/catalog"
	"rllmx/app/llm/upstream"
	"rllmx/app/store/repository"
)

// declaredRunner loads the capability flags stored for custom models once, when
// the stores are open. Without it a stored flag decides nothing after a restart
// until the models page is opened.
type declaredRunner struct {
	app    foundation.Application
	caps   *upstream.Capabilities
	log    log.Log
	ctx    context.Context
	cancel context.CancelFunc
}

func newDeclaredRunner(app foundation.Application, h *HTTP) *declaredRunner {
	ctx, cancel := context.WithCancel(app.Context())
	return &declaredRunner{app: app, caps: h.Capabilities, log: app.MakeLog(), ctx: ctx, cancel: cancel}
}

func (r *declaredRunner) Signature() string { return "rllmx.declared" }
func (r *declaredRunner) ShouldRun() bool   { return true }

// Run loads once and returns: nothing waits on it, and a request reads the
// resolver, never the store.
func (r *declaredRunner) Run() error {
	if !DataOf(r.app).Gate.Wait(r.ctx) {
		return nil
	}
	store := DataOf(r.app).Runner.Store()
	if store == nil || r.caps == nil {
		return nil
	}
	if err := catalog.LoadDeclared(r.ctx, store.KV(), r.caps); err != nil && r.log != nil {
		r.log.Warningf("providers: loading custom model capabilities failed: %v", err)
	}
	return nil
}

func (r *declaredRunner) Shutdown() error {
	r.cancel()
	return nil
}

const (
	// declaredMisses bounds the models remembered as having no custom row, so a
	// request-chosen name cannot grow the set without end.
	declaredMisses = 1024
	// declaredMissFor is how long an absent row is believed, so one the operator
	// adds is found without a restart.
	declaredMissFor = time.Minute
)

// declaredStore writes a capability a custom model proved back into its stored
// row, so the next request orders it as one. A model with no custom row is left
// alone: a built-in is never shadowed by a learned flag.
type declaredStore struct {
	store *repository.Store
	reg   *upstream.Registry
	caps  *upstream.Capabilities

	mu     sync.Mutex
	absent map[string]time.Time
}

// Remember reports whether the model has a custom row. A model without one is
// asked about once, so an image request to a built-in model costs no read.
func (d *declaredStore) Remember(ctx context.Context, provider, model string, in upstream.Declared) (bool, error) {
	if d.store == nil || d.reg == nil || d.caps == nil {
		return false, nil
	}
	missKey := provider + "/" + model
	d.mu.Lock()
	until, known := d.absent[missKey]
	d.mu.Unlock()
	if known && time.Now().Before(until) {
		return false, nil
	}
	owner := provider
	if e, ok := d.reg.ByID(provider); ok && e.Alias != "" {
		owner = e.Alias
	}
	key, body, ok, err := d.row(ctx, owner, provider, model)
	if err != nil {
		return false, err
	}
	if !ok {
		d.mu.Lock()
		if len(d.absent) >= declaredMisses {
			clear(d.absent)
		}
		if d.absent == nil {
			d.absent = map[string]time.Time{}
		}
		d.absent[missKey] = time.Now().Add(declaredMissFor)
		d.mu.Unlock()
		return false, nil
	}
	patched := []byte(body)
	for name, flag := range map[string]*bool{"vision": in.Vision, "reasoning": in.Reasoning, "audio": in.Audio} {
		if flag == nil {
			continue
		}
		if patched, err = sjson.SetBytes(patched, "caps."+name, *flag); err != nil {
			return false, err
		}
	}
	if err := d.store.KV().Set(ctx, repository.ScopeCustomModels, key, string(patched)); err != nil {
		return false, err
	}
	d.caps.Declare(provider, model, in)
	return true, nil
}

// row reads the custom row a learned flag belongs to: the dashboard stores the
// provider's alias, and an earlier write may have used the id.
func (d *declaredStore) row(ctx context.Context, owner, provider, model string) (string, string, bool, error) {
	for _, o := range []string{owner, provider} {
		key := catalog.CustomKey(o, model, upstream.KindLLM)
		body, ok, err := d.store.KV().Get(ctx, repository.ScopeCustomModels, key)
		if err != nil {
			return "", "", false, err
		}
		if ok {
			return key, body, true, nil
		}
	}
	return "", "", false, nil
}
