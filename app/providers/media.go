package providers

import (
	"context"
	"time"

	"github.com/goravel/framework/contracts/foundation"

	"rllmx/app/gateway/account"
	gatewaymedia "rllmx/app/gateway/media"
	mediamux "rllmx/app/llm/media/mux"
	"rllmx/app/platform/network"
)

// MediaSource builds the media endpoints' pipeline on first use, for the same
// reason the chat pipeline is lazy: the stores open after the routes are
// declared.
type MediaSource struct{ lazy[gatewaymedia.Pipeline] }

// NewMediaSource returns a source that reads the bound pieces from app.
func NewMediaSource(app foundation.Application) *MediaSource {
	return &MediaSource{lazy[gatewaymedia.Pipeline]{build: func(ctx context.Context) (*gatewaymedia.Pipeline, error) {
		return buildMedia(ctx, app)
	}}}
}

// Get returns the pipeline, building it if no earlier call did.
func (s *MediaSource) Get() (*gatewaymedia.Pipeline, error) { return s.get() }

func buildMedia(_ context.Context, app foundation.Application) (*gatewaymedia.Pipeline, error) {
	h := HTTPOf(app)
	runner := DataOf(app).Runner
	store, writer := runner.Store(), runner.Writer()
	if store == nil || writer == nil {
		return nil, ErrStoreNotOpen
	}
	log := app.MakeLog()
	client := network.NewClient(network.Options{
		ConnectTimeout: h.Upstream.ConnectTimeout, MaxRedirects: -1, Allow: h.Upstream.Allow, BodyCap: -1,
		Curves: h.Upstream.Curves,
	})
	deps := gatewaymedia.Deps{
		Registry: h.Registry,
		Adapters: mediamux.New(),
		Client:   gatewaymedia.HTTP{Client: client},
		Selector: account.NewSelector(account.SelectorOptions{Conns: store.Connections(), Settings: store.Settings(), Log: log}),
		Reporter: account.NewReporter(store.Connections(), log, nil),
		Nodes:    store.Nodes(),
		Usage:    writer,
		Log:      log,
		Now:      time.Now,
	}
	if h.Refresher != nil {
		deps.Refresher = h.Refresher
	}
	if h.Minter != nil {
		deps.Minter = h.Minter
	}
	return gatewaymedia.New(deps), nil
}
