package providers

import (
	"context"
	"net/http"
	"time"

	"github.com/goravel/framework/contracts/foundation"

	"rllmx/app/llm/upstream"
	"rllmx/app/platform/network"
)

const (
	claudeCLIRefresh = 6 * time.Hour
	claudeCLIRetry   = 10 * time.Minute
)

// claudeRunner keeps the advertised Claude Code version current. It runs on its
// own timer so no request ever waits on npm; a pinned version starts nothing.
type claudeRunner struct {
	cli       *upstream.ClaudeCLI
	newClient func() *http.Client
	ctx       context.Context
	cancel    context.CancelFunc
}

func newClaudeRunner(app foundation.Application, h *HTTP) *claudeRunner {
	ctx, cancel := context.WithCancel(app.Context())
	r := &claudeRunner{cli: h.ClaudeCLI, ctx: ctx, cancel: cancel}
	r.newClient = func() *http.Client {
		return network.NewClient(network.Options{
			ConnectTimeout: h.Upstream.ConnectTimeout, MaxRedirects: 3, Allow: h.Upstream.Allow, BodyCap: -1, Curves: h.Upstream.Curves,
		})
	}
	return r
}

func (r *claudeRunner) Signature() string { return "rllmx.claude-cli" }
func (r *claudeRunner) ShouldRun() bool   { return true }

// Run refreshes once, then every six hours; a failed read retries in ten
// minutes. It returns when shutdown cancels it.
func (r *claudeRunner) Run() error {
	if r.cli.Pinned() {
		return nil
	}
	client := r.newClient()
	for {
		delay := claudeCLIRefresh
		if !r.cli.Refresh(r.ctx, client) {
			delay = claudeCLIRetry
		}
		t := time.NewTimer(delay)
		select {
		case <-r.ctx.Done():
			t.Stop()
			return nil
		case <-t.C:
		}
	}
}

func (r *claudeRunner) Shutdown() error {
	r.cancel()
	return nil
}
