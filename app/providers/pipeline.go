package providers

import (
	"crypto/rsa"

	"context"
	"errors"
	"rllmx/app/llm/zed"
	"rllmx/app/protocol/cosy"

	"github.com/goravel/framework/contracts/foundation"

	"rllmx/app/gateway/account"
	"rllmx/app/gateway/pipeline"
	"rllmx/app/gateway/prefetch"
	"rllmx/app/llm/catalog"
	"rllmx/app/llm/executor"
	"rllmx/app/llm/executor/mux"
	"rllmx/app/llm/executor/opencode"
	"rllmx/app/llm/routing"
	"rllmx/app/llm/stream"
	"rllmx/app/llm/upstream"
	"rllmx/app/platform/cache"
	"rllmx/app/platform/network"
	"rllmx/app/platform/settings"
	"rllmx/app/store/repository"
)

// ErrStoreNotOpen means a request arrived before the data runner opened the stores.
var ErrStoreNotOpen = errors.New("providers: the stores are not open")

// PipelineSource builds the request pipeline on first use. The stores open in a
// runner after every provider registered, so the pipeline cannot be built when
// the routes are declared. A failed build is not kept; the next request retries.
type PipelineSource struct{ lazy[pipeline.Pipeline] }

// NewPipelineSource returns a source that reads the bound pieces from app.
func NewPipelineSource(app foundation.Application) *PipelineSource {
	return &PipelineSource{lazy[pipeline.Pipeline]{build: func(ctx context.Context) (*pipeline.Pipeline, error) { return buildPipeline(ctx, app) }}}
}

// Get returns the pipeline, building it if no earlier call did.
func (s *PipelineSource) Get() (*pipeline.Pipeline, error) { return s.get() }

func buildPipeline(ctx context.Context, app foundation.Application) (*pipeline.Pipeline, error) {
	h := HTTPOf(app)
	runner := DataOf(app).Runner
	store, writer := runner.Store(), runner.Writer()
	if store == nil || writer == nil {
		return nil, ErrStoreNotOpen
	}
	log := app.MakeLog()

	table, err := upstream.DefaultTable(h.Registry)
	if err != nil {
		return nil, err
	}
	if raw, err := store.KV().List(ctx, repository.ScopePricing); err != nil {
		log.Warningf("pipeline: pricing overrides not read: %v", err)
	} else {
		table = table.WithOverrides(upstream.ParseOverrides(raw, func(format string, args ...any) { log.Warningf(format, args...) }))
	}

	settingsRead := settingsFor(store)

	client := network.NewClient(network.Options{
		ConnectTimeout: h.Upstream.ConnectTimeout, MaxRedirects: -1, Allow: h.Upstream.Allow, BodyCap: -1,
		Curves: h.Upstream.Curves,
	})
	// The free lane's gate reads the TLS offer itself, so that one lane goes
	// through a client that lists the suites the tool's own client lists.
	freeLane := network.NewClient(network.Options{
		ConnectTimeout: h.Upstream.ConnectTimeout, MaxRedirects: -1, Allow: h.Upstream.Allow, BodyCap: -1,
		CipherSuites: opencode.OpenCodeCipherSuites, Curves: h.Upstream.Curves,
	})
	return pipeline.New(pipeline.Deps{
		Resolver: &routing.Resolver{Registry: h.Registry, Snapshot: routing.NewSnapshot(routing.NewRepositorySource(store), nil)},
		Selector: account.NewSelector(account.SelectorOptions{Conns: store.Connections(), Settings: store.Settings(), Log: log}),
		Reporter: account.NewReporter(store.Connections(), log, nil),
		Executor: mux.New(&executor.Default{Client: client}, mux.Options{
			PeekIdle: h.Stream.Stall, VertexTokens: h.Google.Vertex, MimoCookies: h.Google.Mimo, OpenCodeClient: freeLane,
			ZedTokens: h.Zed, FreebuffSessions: h.Freebuff, QoderKey: qoderKey(h.Registry), Cache: cacheOrNil(app),
		}),
		Nodes:         store.Nodes(),
		Registry:      h.Registry,
		Caps:          h.Capabilities,
		Prices:        table,
		Usage:         writer,
		Overrides:     headerOverrides{settings: settingsRead},
		Settings:      settingsRead,
		Prefetch:      prefetch.New(),
		ResponseCache: responseCache(app, store),
		Signatures:    signatureCache(app),
		Replay:        signatureCache(app),
		Refresher:     h.Refresher,
		Minter:        h.Minter,
		Projects:      h.Google.Projects,
		Targets:       zed.Targets{Client: h.Zed},
		Catalog:       &catalog.Guard{Cache: cacheOrNil(app), Fetchers: catalog.Fetchers(h.Registry, client)},
		Quota:         h.Google.Quota,
		Timers:        stream.Timers{FirstChunk: h.Stream.FirstChunk, Stall: h.Stream.Stall, UsageWait: h.Stream.UsageWait},
		Log:           log,
	})
}

// qoderKey reads the signing key the gateway's providers carry. It is public
// data, and a file whose key does not parse failed the load already.
func qoderKey(reg *upstream.Registry) *rsa.PublicKey {
	for _, id := range []string{"qoder", "qoder-cn"} {
		e, ok := reg.ByID(id)
		if !ok || e.Transport.SigningKey == "" {
			continue
		}
		if key, err := cosy.ParseKey(e.Transport.SigningKey); err == nil {
			return key
		}
	}
	return nil
}

// headerOverrides gives the operator's extra headers for a provider, read from
// the settings on each call so a dashboard change applies to the next request.
type headerOverrides struct{ settings settingsReader }

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

// signatureCache is the cache the Gemini signature store reads and writes. A
// cache that was never bound gives nil, and signatures are not restored.
func signatureCache(app foundation.Application) pipeline.Loader {
	if c := cacheOrNil(app); c != nil {
		return c
	}
	return nil
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
		// A settings write purges the answers; the budget must forget them too.
		store.OnSettingsChange(rc.Budget.Reset)
	}
	return rc
}
