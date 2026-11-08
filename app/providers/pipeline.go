package providers

import (
	"context"
	"errors"

	"github.com/goravel/framework/contracts/foundation"

	"rllmx/app/account"
	"rllmx/app/cache"
	"rllmx/app/executor"
	"rllmx/app/network"
	"rllmx/app/pipeline"
	"rllmx/app/prefetch"
	"rllmx/app/repository"
	"rllmx/app/routing"
	"rllmx/app/settings"
	"rllmx/app/stream"
	"rllmx/app/upstream"
)

// ErrStoreNotOpen means a request arrived before the data runner opened the stores.
var ErrStoreNotOpen = errors.New("providers: the stores are not open")

// PipelineSource builds the request pipeline on first use. The stores open in a
// runner after every provider registered, so the pipeline cannot be built when
// the routes are declared. A failed build is not kept; the next request retries.
type PipelineSource struct{ lazy[pipeline.Pipeline] }

// NewPipelineSource returns a source that reads the bound pieces from app.
func NewPipelineSource(app foundation.Application) *PipelineSource {
	return &PipelineSource{lazy[pipeline.Pipeline]{build: func() (*pipeline.Pipeline, error) { return buildPipeline(app) }}}
}

// Get returns the pipeline, building it if no earlier call did.
func (s *PipelineSource) Get() (*pipeline.Pipeline, error) { return s.get() }

func buildPipeline(app foundation.Application) (*pipeline.Pipeline, error) {
	h := HTTPOf(app)
	runner := DataOf(app).Runner
	store, writer := runner.Store(), runner.Writer()
	if store == nil || writer == nil {
		return nil, ErrStoreNotOpen
	}
	log := app.MakeLog()
	ctx := context.Background()

	table, err := upstream.DefaultTable(h.Registry)
	if err != nil {
		return nil, err
	}
	if raw, err := store.KV().List(ctx, repository.ScopePricing); err != nil {
		log.Warningf("pipeline: pricing overrides not read: %v", err)
	} else {
		table = table.WithOverrides(upstream.ParseOverrides(raw, func(format string, args ...any) { log.Warningf(format, args...) }))
	}

	client := network.NewClient(network.Options{
		ConnectTimeout: h.Upstream.ConnectTimeout, MaxRedirects: -1, Allow: h.Upstream.Allow, BodyCap: -1,
	})
	return pipeline.New(pipeline.Deps{
		Resolver:      &routing.Resolver{Registry: h.Registry, Snapshot: routing.NewSnapshot(routing.NewRepositorySource(store), nil)},
		Selector:      account.NewSelector(account.SelectorOptions{Conns: store.Connections(), Settings: store.Settings(), Log: log}),
		Reporter:      account.NewReporter(store.Connections(), log, nil),
		Executor:      &executor.Default{Client: client},
		Nodes:         store.Nodes(),
		Registry:      h.Registry,
		Caps:          h.Capabilities,
		Prices:        table,
		Usage:         writer,
		Overrides:     headerOverrides{settings: store.Settings()},
		Settings:      store.Settings(),
		Prefetch:      prefetch.New(),
		ResponseCache: responseCache(app, store),
		Refresher:     h.Refresher,
		Timers:        stream.Timers{FirstChunk: h.Stream.FirstChunk, Stall: h.Stream.Stall, UsageWait: h.Stream.UsageWait},
		Log:           log,
	})
}

// headerOverrides gives the operator's extra headers for a provider, read from
// the settings on each call so a dashboard change applies to the next request.
type headerOverrides struct{ settings *repository.SettingsRepo }

func (o headerOverrides) Headers(ctx context.Context, provider string) map[string]string {
	doc, err := o.settings.Effective(ctx)
	if err != nil {
		return nil
	}
	raw, ok := doc.GetRaw("providerOverrides")
	if !ok {
		return nil
	}
	return upstream.HeaderOverrides(raw, provider)
}

// responseCache wires the opt-in answer cache. The in-process store is bounded
// by the configured limits; with a shared store the server's own limit applies,
// so no budget is set. A cache that was never bound leaves the feature off.
func responseCache(app foundation.Application, store *repository.Store) *pipeline.ResponseCache {
	c := cacheOrNil(app)
	if c == nil {
		return nil
	}
	rc := &pipeline.ResponseCache{Cache: c, Flags: store.KV()}
	if lim, err := settings.LoadCache(app.MakeConfig()); err == nil && !lim.Shared {
		rc.Budget = &cache.Budget{MaxEntries: lim.ResponseMaxEntries, MaxBytes: lim.ResponseMaxBytes}
	}
	return rc
}
