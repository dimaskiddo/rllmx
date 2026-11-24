package providers

import (
	"context"
	"net/url"
	"time"

	"github.com/goravel/framework/contracts/foundation"

	"rllmx/app/gateway/account"
	gatewaymedia "rllmx/app/gateway/media"
	mediamux "rllmx/app/llm/media/mux"
	"rllmx/app/llm/media/voice"
	"rllmx/app/platform/network"
	"rllmx/app/store/document"
	"rllmx/app/store/repository"
)

// hostOf is the host name of an operator-set address; the settings checked it,
// so an unparsable value yields nothing to permit.
func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// rotatedCredential stores a cookie a web provider rotated during a call. It is
// the one writer the media pipeline needs; every other credential is written by
// its own controller.
type rotatedCredential struct{ conns *repository.ConnectionsRepo }

func (r rotatedCredential) UpdateApiKey(ctx context.Context, connID, key string) error {
	_, err := r.conns.Update(ctx, connID, repository.ConnPatch{
		Fields: map[string]string{"apiKey": document.Encode(key)},
	})
	return err
}

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
	allow := h.Upstream.Allow
	if h.Media != nil && h.Media.SearxngURL != "" {
		// The operator named the instance; its host is permitted for these calls
		// and for nothing else, the way the health probe permits its own host.
		allow = allow.With(hostOf(h.Media.SearxngURL))
	}
	client := network.NewClient(network.Options{
		ConnectTimeout: h.Upstream.ConnectTimeout, MaxRedirects: -1, Allow: allow, BodyCap: -1,
		Curves: h.Upstream.Curves,
	})
	// A fetch target is read directly for the built-in converter, so it follows
	// up to five redirects: every hop dials through the guard. A client names the
	// page and gets its text back, so the operator's private list does not apply
	// here: it names providers the operator runs, not pages a caller may read.
	fetchClient := network.NewClient(network.Options{
		ConnectTimeout: h.Upstream.ConnectTimeout, MaxRedirects: 5, BodyCap: -1,
		Curves: h.Upstream.Curves,
	})
	deps := gatewaymedia.Deps{
		Registry:    h.Registry,
		Adapters:    mediamux.New(mediamux.Options{VertexTokens: h.Google.Vertex}),
		Client:      gatewaymedia.HTTP{Client: client},
		FetchClient: gatewaymedia.HTTP{Client: fetchClient, Cap: gatewaymedia.MaxFetchBytes},
		Selector:    account.NewSelector(account.SelectorOptions{Conns: store.Connections(), Settings: store.Settings(), Log: log}),
		Reporter:    account.NewReporter(store.Connections(), log, nil),
		Nodes:       store.Nodes(),
		Connections: store.Connections(),
		Credentials: rotatedCredential{conns: store.Connections()},
		Voices:      voice.List,
		Usage:       writer,
		Budget:      Budget(h.Limits, runner),
		Log:         log,
		Now:         time.Now,
		Network:     network.Options{ConnectTimeout: h.Upstream.ConnectTimeout, Allow: h.Upstream.Allow, Curves: h.Upstream.Curves},
	}
	if h.Media != nil {
		deps.VideoTimeout = h.Media.VideoTimeout
		deps.SearxngURL = h.Media.SearxngURL
	}
	if h.Refresher != nil {
		deps.Refresher = h.Refresher
	}
	if h.Minter != nil {
		deps.Minter = h.Minter
	}
	return gatewaymedia.New(deps), nil
}
