package controllers

import (
	"context"
	"errors"
	nethttp "net/http"

	contractshttp "github.com/goravel/framework/contracts/http"

	"rllmx/app/llm/upstream"
)

// CatalogSyncer runs one catalog sync and reports its state. It is an interface
// here so a test answers without a network.
type CatalogSyncer interface {
	Run(ctx context.Context) (upstream.SyncResult, error)
	State() upstream.SyncState
}

func (m *ModelsController) syncer() CatalogSyncer {
	if m.CatalogSync == nil {
		return nil
	}
	return m.CatalogSync()
}

// CatalogStatus answers GET /api/models/catalog-sync with the last sync and what
// the installed catalog holds.
func (m *ModelsController) CatalogStatus(ctx contractshttp.Context) contractshttp.Response {
	s := m.syncer()
	if s == nil {
		return flat(ctx, nethttp.StatusServiceUnavailable, "Catalog sync is not available")
	}
	return ctx.Response().Json(nethttp.StatusOK, s.State())
}

// CatalogRun answers POST /api/models/catalog-sync: it syncs now instead of
// waiting for the timer.
func (m *ModelsController) CatalogRun(ctx contractshttp.Context) contractshttp.Response {
	s := m.syncer()
	if s == nil {
		return flat(ctx, nethttp.StatusServiceUnavailable, "Catalog sync is not available")
	}
	res, err := s.Run(ctx.Request().Origin().Context())
	if errors.Is(err, upstream.ErrSyncRunning) {
		return flat(ctx, nethttp.StatusServiceUnavailable, "sync in progress")
	}
	if err != nil {
		if m.Log != nil {
			m.Log.Warningf("models: catalog sync: %v", err)
		}
		return flat(ctx, nethttp.StatusServiceUnavailable, err.Error())
	}
	return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{"success": true, "result": res})
}
