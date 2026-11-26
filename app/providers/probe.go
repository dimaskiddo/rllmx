package providers

import (
	"context"
	"sync"

	"github.com/goravel/framework/contracts/foundation"

	"rllmx/app/gateway/catalog"
	"rllmx/app/gateway/probe"
	"rllmx/app/http/controllers"
	"rllmx/app/store/repository"
)

// queueCap bounds the connections waiting for a probe. A full queue drops a
// job: the dashboard save that asked for it is already answered, and a later
// change enqueues again.
const queueCap = 64

// ProbeSource builds the model prober on first use and owns the connections
// waiting for one. The stores open in a runner after the routes are declared, so
// the prober cannot be built with them.
type ProbeSource struct {
	lazy[Probes]
	q *probeQueue
}

// Probes is the prober the dashboard routes call.
type Probes struct {
	Prober *probe.Prober
}

// NewProbeSource returns a source over the app's stores and the request
// pipeline.
func NewProbeSource(app foundation.Application, pipe *PipelineSource) *ProbeSource {
	s := &ProbeSource{q: newProbeQueue()}
	s.build = func(context.Context) (*Probes, error) { return buildProbes(app, pipe) }
	return s
}

func buildProbes(app foundation.Application, pipe *PipelineSource) (*Probes, error) {
	store := DataOf(app).Runner.Store()
	if store == nil {
		return nil, ErrStoreNotOpen
	}
	p := probe.New(probe.Options{
		Pipeline: func() (probe.Handler, error) { return pipe.Get() },
		Nodes:    func() probe.NodeReader { return store.Nodes() },
		KV:       func() catalog.KV { return store.KV() },
		Registry: HTTPOf(app).Registry,
	})
	return &Probes{Prober: p}, nil
}

// Get returns the prober, building it if no earlier call did.
func (s *ProbeSource) Get() (*Probes, error) { return s.get() }

// Enqueue asks for a probe of one connection. It never blocks and never
// duplicates a pending id.
func (s *ProbeSource) Enqueue(id string) {
	if s != nil {
		s.q.enqueue(id)
	}
}

// probeQueue is the bounded list of connections waiting for a probe.
type probeQueue struct {
	ch chan string
	mu sync.Mutex
	// pending holds the ids already queued, so a create and the update that
	// follows it are one job.
	pending map[string]bool
}

func newProbeQueue() *probeQueue {
	return &probeQueue{ch: make(chan string, queueCap), pending: map[string]bool{}}
}

// enqueue never blocks: a full queue drops the job, because the request that
// asked for it is already answered and a later change enqueues again.
func (q *probeQueue) enqueue(id string) {
	if id == "" {
		return
	}
	q.mu.Lock()
	if q.pending[id] {
		q.mu.Unlock()
		return
	}
	q.pending[id] = true
	q.mu.Unlock()
	select {
	case q.ch <- id:
	default:
		q.mu.Lock()
		delete(q.pending, id)
		q.mu.Unlock()
	}
}

// next takes one queued connection, or reports that shutdown happened.
func (q *probeQueue) next(ctx context.Context) (string, bool) {
	select {
	case <-ctx.Done():
		return "", false
	case id := <-q.ch:
		q.mu.Lock()
		delete(q.pending, id)
		q.mu.Unlock()
		return id, true
	}
}

// len is how many ids are waiting; a test asserts the bound.
func (q *probeQueue) len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.pending)
}

// probeRunner drains the probe queue one connection at a time, so a bulk
// import cannot fan out into hundreds of provider calls.
type probeRunner struct {
	app    foundation.Application
	probes *ProbeSource
	// check judges a connection's credential when it has no model to ping; nil
	// leaves that fallback out.
	check  func(ctx context.Context, st *repository.Store, conn repository.Connection) error
	ctx    context.Context
	cancel context.CancelFunc
}

func newProbeRunner(app foundation.Application, probes *ProbeSource, h *HTTP) *probeRunner {
	ctx, cancel := context.WithCancel(app.Context())
	ctl := &controllers.ProvidersController{Registry: h.Registry, Checker: h.Checker, Refresher: h.Refresher}
	return &probeRunner{app: app, probes: probes, check: ctl.CheckStored, ctx: ctx, cancel: cancel}
}

func (r *probeRunner) Signature() string { return "rllmx.probe" }
func (r *probeRunner) ShouldRun() bool   { return true }

// Run probes queued connections until shutdown. A probe that fails leaves the
// connection to the next attempt; the dashboard already answered.
func (r *probeRunner) Run() error {
	for {
		id, ok := r.probes.q.next(r.ctx)
		if !ok {
			return nil
		}
		r.one(id)
	}
}

func (r *probeRunner) one(id string) {
	store := DataOf(r.app).Runner.Store()
	if store == nil {
		return
	}
	conn, found, err := store.Connections().Get(r.ctx, id)
	if err != nil || !found || conn.Corrupt || !conn.IsActive {
		return
	}
	built, err := r.probes.Get()
	if err != nil {
		return
	}
	var check func(context.Context) error
	if r.check != nil {
		check = func(ctx context.Context) error { return r.check(ctx, store, conn) }
	}
	probeThenCheck(r.ctx, built.Prober, conn, check)
}

// connProber is the one method the runner needs from the prober.
type connProber interface {
	Connection(ctx context.Context, conn repository.Connection, force bool) []probe.Result
}

// probeThenCheck pings a connection and checks its key when there is no model
// to ping, so a connection with nothing to call still reports on its status.
func probeThenCheck(ctx context.Context, p connProber, conn repository.Connection, check func(context.Context) error) {
	if len(p.Connection(ctx, conn, false)) > 0 || check == nil {
		return
	}
	_ = check(ctx)
}

func (r *probeRunner) Shutdown() error {
	r.cancel()
	return nil
}

// Prober returns the prober alone, for the dashboard tests.
func (s *ProbeSource) Prober() (*probe.Prober, error) {
	p, err := s.get()
	if err != nil {
		return nil, err
	}
	return p.Prober, nil
}
