package controllers

import (
	"context"
	"errors"
	nethttp "net/http"
	"testing"

	contractshttp "github.com/goravel/framework/contracts/http"

	apphttp "rllmx/app/http"
	"rllmx/app/llm/upstream"
)

// catalogFake answers the two catalog routes without a network.
type catalogFake struct {
	state upstream.SyncState
	res   upstream.SyncResult
	err   error
	runs  int
}

func (f *catalogFake) Run(context.Context) (upstream.SyncResult, error) {
	f.runs++
	return f.res, f.err
}

func (f *catalogFake) State() upstream.SyncState { return f.state }

func newCatalogRig(t *testing.T, f *catalogFake) *mgmtRig {
	t.Helper()
	m := newMgmtRig(t)
	c := &ModelsController{CatalogSync: func() CatalogSyncer { return f }}
	h := func(method, path string, fn func(contractshttp.Context) contractshttp.Response) {
		m.guard.Handle(m.router, apphttp.ClassProtected, method, path, fn)
	}
	h(nethttp.MethodGet, "/api/models/catalog-sync", c.CatalogStatus)
	h(nethttp.MethodPost, "/api/models/catalog-sync", c.CatalogRun)
	return m
}

func TestTheCatalogStatusAnswersWhatTheSyncReports(t *testing.T) {
	f := &catalogFake{state: upstream.SyncState{
		LastSync: 1700, FileVersion: upstream.OverlayVersion, File: upstream.OverlayFile,
		URL: upstream.ModelsDevURL, IntervalMs: 86400000,
		Catalog: &upstream.Summary{SyncedAt: 1700, Models: 3, Providers: 2, Bytes: 900},
	}}
	m := newCatalogRig(t, f)
	out := m.ok(t, "GET", "/api/models/catalog-sync", "", 200)
	if out["fileVersion"].(float64) != upstream.OverlayVersion || out["file"] != upstream.OverlayFile {
		t.Fatalf("answer %v", out)
	}
	cat, _ := out["catalog"].(map[string]any)
	if cat == nil || cat["models"].(float64) != 3 || cat["providers"].(float64) != 2 {
		t.Fatalf("catalog %v", out["catalog"])
	}
}

func TestTheCatalogStatusNamesNoLocalPath(t *testing.T) {
	f := &catalogFake{}
	m := newCatalogRig(t, f)
	out := m.ok(t, "GET", "/api/models/catalog-sync", "", 200)
	if file, _ := out["file"].(string); file != "" && file != upstream.OverlayFile {
		t.Fatalf("file %q", file)
	}
}

func TestRunningTheSyncNowAnswersItsResult(t *testing.T) {
	f := &catalogFake{res: upstream.SyncResult{Status: "updated", Models: 4, Providers: 2, Bytes: 700}}
	m := newCatalogRig(t, f)
	out := m.ok(t, "POST", "/api/models/catalog-sync", "", 200)
	if out["success"] != true || f.runs != 1 {
		t.Fatalf("answer %v runs %d", out, f.runs)
	}
	res, _ := out["result"].(map[string]any)
	if res["status"] != "updated" || res["models"].(float64) != 4 {
		t.Fatalf("result %v", out["result"])
	}
}

func TestARunningSyncAnswersServiceUnavailable(t *testing.T) {
	f := &catalogFake{err: upstream.ErrSyncRunning}
	m := newCatalogRig(t, f)
	out := m.ok(t, "POST", "/api/models/catalog-sync", "", 503)
	if out["error"] != "sync in progress" {
		t.Fatalf("error %v", out["error"])
	}
}

func TestAFailedSyncAnswersItsClass(t *testing.T) {
	f := &catalogFake{err: errors.New("upstream: the catalog answered 502")}
	m := newCatalogRig(t, f)
	out := m.ok(t, "POST", "/api/models/catalog-sync", "", 503)
	if msg, _ := out["error"].(string); msg == "" {
		t.Fatalf("answer %v", out)
	}
}

func TestTheCatalogRoutesAnswerUnavailableWithoutASyncer(t *testing.T) {
	m := newMgmtRig(t)
	c := &ModelsController{}
	m.guard.Handle(m.router, apphttp.ClassProtected, nethttp.MethodGet, "/api/models/catalog-sync", c.CatalogStatus)
	m.guard.Handle(m.router, apphttp.ClassProtected, nethttp.MethodPost, "/api/models/catalog-sync", c.CatalogRun)
	m.ok(t, "GET", "/api/models/catalog-sync", "", 503)
	m.ok(t, "POST", "/api/models/catalog-sync", "", 503)
}
