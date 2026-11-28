package providers

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/goravel/framework/contracts/log"

	"rllmx/app/llm/upstream"
	appfile "rllmx/app/platform/file"
	"rllmx/app/platform/text"
)

const (
	catalogStartup  = time.Minute
	catalogInterval = 24 * time.Hour
	catalogRetry    = 30 * time.Minute
	// catalogTimeout keeps one sync under the dashboard's 60 s write deadline.
	catalogTimeout = 50 * time.Second
	maxErrorText   = 200
)

// CatalogSync owns the synced catalog file and the state the dashboard reads.
// The overlay itself lives in the capability tables, so a request never waits on
// a sync.
type CatalogSync struct {
	Caps   *upstream.Capabilities
	Dir    string
	Client func() *http.Client
	Log    log.Log
	// Enabled is the CATALOG_SYNC switch: false loads the file and stops the
	// timer.
	Enabled bool

	mu      sync.Mutex
	state   upstream.SyncState
	running atomic.Bool
}

func newCatalogSync(caps *upstream.Capabilities, dir string, client func() *http.Client, l log.Log) *CatalogSync {
	return &CatalogSync{
		Caps: caps, Dir: dir, Client: client, Log: l,
		state: upstream.SyncState{
			File: upstream.OverlayFile, URL: upstream.ModelsDevURL,
			IntervalMs: catalogInterval.Milliseconds(),
		},
	}
}

// Load installs the file a previous run wrote. A file this build did not write,
// or one it cannot read, leaves the tables deciding alone.
func (s *CatalogSync) Load() {
	if s == nil || s.Caps == nil || s.Dir == "" {
		return
	}
	o, raw, err := upstream.ReadOverlayFile(filepath.Join(s.Dir, upstream.OverlayFile))
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			s.warn("catalog file ignored: %v", err)
		}
		return
	}
	s.Caps.SetOverlay(o)
	sum := o.Summary()
	sum.Bytes = len(raw)
	s.mu.Lock()
	s.state.ETag = o.ETag
	s.state.FileVersion = o.Version
	s.state.Catalog = &sum
	if o.SyncedAt > s.state.LastSync {
		s.state.LastSync = o.SyncedAt
	}
	s.mu.Unlock()
}

// Run performs one sync: fetch, build the deltas, write the file and install the
// overlay. A second caller is refused while one runs, and the caller's deadline
// bounds it.
func (s *CatalogSync) Run(ctx context.Context) (upstream.SyncResult, error) {
	if s == nil || s.Caps == nil || s.Client == nil {
		return upstream.SyncResult{}, errors.New("providers: no catalog sync is wired")
	}
	if !s.running.CompareAndSwap(false, true) {
		return upstream.SyncResult{}, upstream.ErrSyncRunning
	}
	defer s.running.Store(false)
	ctx, cancel := context.WithTimeout(ctx, catalogTimeout)
	defer cancel()
	// The ETag is empty unless the stored file is one this build wrote, so a
	// foreign file always earns a full fetch.
	s.mu.Lock()
	etag := s.state.ETag
	s.mu.Unlock()
	overlay, raw, res, err := s.Caps.FetchOverlay(ctx, s.Client(), etag)
	if err != nil {
		s.failed(err)
		return upstream.SyncResult{}, err
	}
	if res.Status == "unchanged" {
		s.done(res, nil, 0)
		return res, nil
	}
	if err := appfile.WritePrivate(s.Dir, upstream.OverlayFile, raw); err != nil {
		s.failed(err)
		return upstream.SyncResult{}, err
	}
	s.Caps.SetOverlay(overlay)
	s.done(res, overlay, len(raw))
	return res, nil
}

// State is a copy of what the status route answers.
func (s *CatalogSync) State() upstream.SyncState {
	if s == nil {
		return upstream.SyncState{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.state
	out.Running = s.running.Load()
	return out
}

func (s *CatalogSync) done(res upstream.SyncResult, o *upstream.Overlay, size int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.LastSync = time.Now().UnixMilli()
	s.state.LastError = ""
	s.state.LastResult = &res
	if res.ETag != "" {
		s.state.ETag = res.ETag
	}
	if o != nil {
		s.state.FileVersion = o.Version
		sum := o.Summary()
		sum.Bytes = size
		s.state.Catalog = &sum
	}
}

func (s *CatalogSync) failed(err error) {
	msg := text.CutBytes(strings.TrimSpace(err.Error()), maxErrorText)
	s.mu.Lock()
	s.state.LastError = msg
	s.mu.Unlock()
	s.warn("catalog sync failed: %v", err)
}

func (s *CatalogSync) warn(format string, args ...any) {
	if s.Log != nil {
		s.Log.Warningf(format, args...)
	}
}
