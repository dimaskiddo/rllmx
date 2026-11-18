package providers

import (
	"context"
	"time"

	"github.com/goravel/framework/contracts/foundation"
)

// catalogRunner installs the catalog file at start and keeps it current. The
// timer is off with CATALOG_SYNC=false; the file is still loaded, so a copy
// already on disk keeps deciding.
type catalogRunner struct {
	sync    *CatalogSync
	enabled bool
	ctx     context.Context
	cancel  context.CancelFunc
}

func newCatalogRunner(app foundation.Application, s *CatalogSync, enabled bool) *catalogRunner {
	ctx, cancel := context.WithCancel(app.Context())
	return &catalogRunner{sync: s, enabled: enabled, ctx: ctx, cancel: cancel}
}

func (r *catalogRunner) Signature() string { return "rllmx.catalog" }
func (r *catalogRunner) ShouldRun() bool   { return true }

// Run loads the file, then syncs every 24 hours, retrying after 30 minutes when
// a sync fails. It returns when shutdown cancels it.
func (r *catalogRunner) Run() error {
	r.sync.Load()
	if !r.enabled {
		return nil
	}
	wait := catalogStartup
	for {
		t := time.NewTimer(wait)
		select {
		case <-r.ctx.Done():
			t.Stop()
			return nil
		case <-t.C:
		}
		wait = catalogInterval
		if _, err := r.sync.Run(r.ctx); err != nil {
			wait = catalogRetry
		}
	}
}

func (r *catalogRunner) Shutdown() error {
	r.cancel()
	return nil
}
