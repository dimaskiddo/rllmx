package providers

import (
	"context"

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

// declaredStore writes a capability a custom model proved back into its stored
// row, so the next request orders it as one. A model with no custom row is left
// alone: a built-in is never shadowed by a learned flag.
type declaredStore struct {
	store *repository.Store
	reg   *upstream.Registry
	caps  *upstream.Capabilities
	log   log.Log
}

func (d declaredStore) Remember(ctx context.Context, provider, model string, in upstream.Declared) error {
	if d.store == nil || d.reg == nil || d.caps == nil {
		return nil
	}
	owner := provider
	if e, ok := d.reg.ByID(provider); ok && e.Alias != "" {
		owner = e.Alias
	}
	key, body, ok, err := d.row(ctx, owner, provider, model)
	if err != nil || !ok {
		return err
	}
	patched, err := sjson.SetBytes([]byte(body), "caps.vision", true)
	if err != nil {
		return err
	}
	if err := d.store.KV().Set(ctx, repository.ScopeCustomModels, key, string(patched)); err != nil {
		return err
	}
	d.caps.Declare(provider, model, in)
	return nil
}

// row reads the custom row a learned flag belongs to: the dashboard stores the
// provider's alias, and an earlier write may have used the id.
func (d declaredStore) row(ctx context.Context, owner, provider, model string) (string, string, bool, error) {
	for _, o := range []string{owner, provider} {
		key := o + "|" + model + "|" + upstream.KindLLM
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
