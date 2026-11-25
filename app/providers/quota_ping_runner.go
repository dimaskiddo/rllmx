package providers

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/goravel/framework/contracts/foundation"

	"rllmx/app/access/identity"
	"rllmx/app/gateway/pipeline"
	"rllmx/app/llm/quota"
	"rllmx/app/llm/stream"
	"rllmx/app/llm/upstream"
	"rllmx/app/store/repository"
)

const (
	// quotaPingInterval is the scheduler tick; a ping happens only after a
	// window's reset, so most ticks do nothing.
	quotaPingInterval = time.Minute
	// quotaPingCooldown keeps a failing provider from being tried every tick.
	quotaPingCooldown = 15 * time.Minute
)

// quotaPingRunner keeps a provider's short window warm: when the window's reset
// has passed and the operator's switch is on, it sends one tiny request through
// the pipeline so the next window starts now. It reads the switch each tick, so
// turning it off stops the pings without a restart.
type quotaPingRunner struct {
	app    foundation.Application
	http   *HTTP
	ctx    context.Context
	cancel context.CancelFunc
	now    func() time.Time

	mu     sync.Mutex
	pinged map[string]string
	failed map[string]time.Time
}

func newQuotaPingRunner(app foundation.Application, h *HTTP) *quotaPingRunner {
	ctx, cancel := context.WithCancel(app.Context())
	return &quotaPingRunner{
		app: app, http: h, ctx: ctx, cancel: cancel, now: time.Now,
		pinged: map[string]string{}, failed: map[string]time.Time{},
	}
}

func (r *quotaPingRunner) Signature() string { return "rllmx.quotaping" }
func (r *quotaPingRunner) ShouldRun() bool   { return true }

// Run waits for the store, then ticks until shutdown.
func (r *quotaPingRunner) Run() error {
	if !DataOf(r.app).Gate.Wait(r.ctx) {
		return nil
	}
	for {
		r.tick(r.ctx)
		t := time.NewTimer(quotaPingInterval)
		select {
		case <-r.ctx.Done():
			t.Stop()
			return nil
		case <-t.C:
		}
	}
}

func (r *quotaPingRunner) Shutdown() error {
	r.cancel()
	return nil
}

// tick pings every window that has reset and has not been warmed yet.
func (r *quotaPingRunner) tick(ctx context.Context) {
	store := DataOf(r.app).Runner.Store()
	if store == nil || r.http == nil || r.http.Quota == nil {
		return
	}
	doc, err := store.Settings().Effective(ctx)
	if err != nil {
		return
	}
	conns, err := store.Connections().List(ctx, repository.ConnFilter{})
	if err != nil {
		return
	}
	now := r.now()
	for _, conn := range conns {
		setting := pingSwitch(conn.Provider)
		if setting == "" || !doc.Bool(setting, false) || !conn.IsActive {
			continue
		}
		if conn.AuthType != "oauth" && conn.AuthType != "access_token" {
			continue
		}
		r.mu.Lock()
		cool := r.failed[conn.ID]
		last := r.pinged[conn.ID]
		r.mu.Unlock()
		if cool.After(now) {
			continue
		}
		reading, err := r.http.Quota.Read(ctx, conn, false)
		if err != nil {
			continue
		}
		key, ok := dueWindow(reading, conn.Provider, now)
		if !ok || key == last {
			continue
		}
		model := r.pingModel(conn.Provider)
		if model == "" {
			continue
		}
		if err := r.ping(ctx, conn.Provider, model); err != nil {
			r.mu.Lock()
			r.failed[conn.ID] = now.Add(quotaPingCooldown)
			r.mu.Unlock()
			continue
		}
		r.mu.Lock()
		r.pinged[conn.ID] = key
		delete(r.failed, conn.ID)
		r.mu.Unlock()
	}
}

// pingSwitch names the operator's switch for a provider, or "" for one this
// runner does not warm.
func pingSwitch(provider string) string {
	switch provider {
	case "claude":
		return "claudeAutoPing"
	case "codex":
		return "codexAutoPing"
	}
	return ""
}

// dueWindow reports the reset a ping is owed for: the provider's short window
// has passed its reset and the reading names it. The key changes with the
// window, so one ping per window is all that happens.
func dueWindow(reading quota.Reading, provider string, now time.Time) (string, bool) {
	for name, q := range reading.Quotas {
		if !shortWindow(provider, name) || q.ResetAt == nil || q.ResetAt.After(now) {
			continue
		}
		return q.ResetAt.UTC().Format(time.RFC3339), true
	}
	return "", false
}

// shortWindow names the window a provider warms: the five-hour session.
func shortWindow(provider, name string) bool {
	id := strings.ToLower(name)
	switch provider {
	case "claude":
		return strings.Contains(id, "session") || strings.Contains(id, "5h")
	case "codex":
		return id == "session"
	}
	return false
}

// pingModel is the provider's first chat model: a pinned id would rot.
func (r *quotaPingRunner) pingModel(provider string) string {
	if r.http == nil || r.http.Registry == nil {
		return ""
	}
	for _, m := range r.http.Registry.Models(provider) {
		if m.Kind == "" || m.Kind == upstream.KindLLM {
			return m.ID
		}
	}
	return ""
}

// ping sends one minimal request through the pipeline and asks nothing of the
// answer. A stream a provider forces is discarded.
func (r *quotaPingRunner) ping(ctx context.Context, provider, model string) error {
	pipe, err := r.http.Pipe.Get()
	if err != nil {
		return err
	}
	body := []byte(`{"model":"` + provider + "/" + model + `","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`)
	in := pipeline.Input{
		Path: "/v1/chat/completions", Body: body, Caller: identity.Probe(),
		RequestID: "autoping-" + strconv.FormatInt(time.Now().UnixNano(), 10),
	}
	out := pipe.Handle(ctx, in, discardOpener)
	if out.Status != http.StatusOK {
		return fmt.Errorf("quota ping: status %d", out.Status)
	}
	return nil
}

// discardWriter swallows an answer the ping does not read.
type discardWriter struct{}

func (discardWriter) Header() http.Header         { return http.Header{} }
func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
func (discardWriter) WriteHeader(int)             {}
func (discardWriter) Flush()                      {}

func discardOpener() (stream.Client, error) {
	return stream.NewSSE(discardWriter{}, stream.Options{})
}
