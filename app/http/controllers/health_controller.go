package controllers

import (
	"context"
	"encoding/json"
	nethttp "net/http"
	"time"

	"github.com/goravel/framework/contracts/http"

	"rllmx/app/gateway/health"
	"rllmx/app/llm/stream"
	"rllmx/app/platform/failure"
	"rllmx/app/store/repository"
)

const (
	// feedEvery is how often the latency feed looks for a change.
	feedEvery = 2 * time.Second
	// feedBeat keeps a proxy from closing an idle feed.
	feedBeat = 30 * time.Second
)

// HealthController answers the liveness route and the health feed. The feed's
// fields are optional: without them the liveness route still answers.
type HealthController struct {
	// DB reads the connections whose providers are always on the feed, so a
	// quiet provider is not mistaken for a removed one.
	DB func() *repository.Store
	// Tracker is the per-provider health table; nil answers an empty feed.
	Tracker *health.Tracker
	// every and beat are the feed's cadence; zero takes the defaults.
	every, beat time.Duration
}

// NewHealthController builds the handler the health route serves.
func NewHealthController() *HealthController {
	return &HealthController{}
}

// Index answers 200 {"ok":true} to anyone, and no body to HEAD.
func (r *HealthController) Index(ctx http.Context) http.Response {
	if ctx.Request().Method() == nethttp.MethodHead {
		return ctx.Response().Header("Content-Type", "application/json").NoContent(nethttp.StatusOK)
	}
	return ctx.Response().Json(nethttp.StatusOK, http.Json{"ok": true})
}

// LatencyStream answers GET /api/health/latency-stream: the health table as it
// is, then again whenever it changes, with a comment every 30 s so a proxy
// keeps the connection open. Unlike the rest of the API it has no write
// deadline; the writer's own timeout bounds a client that stopped reading.
func (r *HealthController) LatencyStream(ctx http.Context) http.Response {
	rctx := ctx.Request().Origin().Context()
	w := stream.WriterOf(ctx)
	sse, err := stream.NewSSE(w, stream.Options{WriteTimeout: stream.DefaultWriteTimeout})
	if err != nil {
		failure.Write(w, failure.ForPath(ctx.Request().Origin().URL.Path), nethttp.StatusInternalServerError, "The feed cannot be started")
		return nil
	}
	seeds := r.seeds(rctx)
	every, interval := r.every, r.beat
	if every <= 0 {
		every = feedEvery
	}
	if interval <= 0 {
		interval = feedBeat
	}
	tick, beat := time.NewTimer(every), time.NewTimer(interval)
	defer tick.Stop()
	defer beat.Stop()
	last := ""
	for {
		payload := r.payload(seeds)
		// The first frame is sent at once, and after that only a change: a feed
		// that repeats itself every two seconds is noise.
		if payload != last {
			if err := sse.Data(payload); err != nil {
				return nil
			}
			last = payload
		}
		select {
		case <-rctx.Done():
			return nil
		case <-tick.C:
			tick.Reset(every)
		case <-beat.C:
			beat.Reset(interval)
			if err := sse.Raw([]byte(": keepalive\n\n")); err != nil {
				return nil
			}
		}
	}
}

// seeds names the providers with an active connection.
func (r *HealthController) seeds(ctx context.Context) []string {
	if r.DB == nil {
		return nil
	}
	st := r.DB()
	if st == nil {
		return nil
	}
	active := true
	conns, err := st.Connections().List(ctx, repository.ConnFilter{IsActive: &active})
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(conns))
	for _, c := range conns {
		if c.Provider != "" && !seen[c.Provider] {
			seen[c.Provider] = true
			out = append(out, c.Provider)
		}
	}
	return out
}

// payload is the feed frame. encoding/json sorts the map, so an unchanged table
// is the same bytes and is not sent twice.
func (r *HealthController) payload(seeds []string) string {
	states := map[string]health.State{}
	if r.Tracker != nil {
		states = r.Tracker.Snapshot()
	}
	for _, p := range seeds {
		if _, ok := states[p]; !ok {
			states[p] = health.State{CircuitState: health.Closed}
		}
	}
	body, err := json.Marshal(http.Json{"providers": states})
	if err != nil {
		return "{}"
	}
	return string(body)
}
