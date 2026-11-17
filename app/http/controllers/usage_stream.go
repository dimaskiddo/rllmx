package controllers

import (
	"encoding/json"
	nethttp "net/http"
	"time"

	contractshttp "github.com/goravel/framework/contracts/http"

	"rllmx/app/llm/stream"
	"rllmx/app/platform/failure"
)

const (
	usageEvery = 150 * time.Millisecond
	usageBeat  = 25 * time.Second
)

// Stream answers GET /api/usage/stream: the live request list on connect, then
// one frame whenever a request starts or ends or a usage batch lands. It sends
// no all-time totals, so a frame costs a tracker read and, for a new batch, one
// small query.
func (c *UsageController) Stream(ctx contractshttp.Context) contractshttp.Response {
	rctx := ctx.Request().Origin().Context()
	w := stream.WriterOf(ctx)
	sse, err := stream.NewSSE(w, stream.Options{WriteTimeout: stream.DefaultWriteTimeout})
	if err != nil {
		failure.Write(w, failure.ForPath(ctx.Request().Origin().URL.Path), nethttp.StatusInternalServerError, "The feed cannot be started")
		return nil
	}
	every, interval := c.every, c.beat
	if every <= 0 {
		every = usageEvery
	}
	if interval <= 0 {
		interval = usageBeat
	}

	accounts := c.accountNames(rctx)
	send := func(recent []usageRecent) error {
		pending, active := c.live(accounts)
		raw, err := json.Marshal(contractshttp.Json{
			"pending": pending, "activeRequests": active, "recentRequests": recent,
			"errorProvider": c.Inflight.ErrorProvider(),
		})
		if err != nil {
			return err
		}
		return sse.Data(string(raw))
	}

	// The counters are read before the first frame: a change that lands while
	// it is written must still produce the next one.
	generation, flushed := c.generation(), c.flushed()
	recent, err := c.recent(rctx)
	if err != nil {
		recent = []usageRecent{}
	}
	if err := send(recent); err != nil {
		return nil
	}

	tick, beat := time.NewTimer(every), time.NewTimer(interval)
	defer tick.Stop()
	defer beat.Stop()
	for {
		select {
		case <-rctx.Done():
			return nil
		case <-tick.C:
			tick.Reset(every)
			nextGeneration, nextFlushed := c.generation(), c.flushed()
			if nextGeneration == generation && nextFlushed == flushed {
				continue
			}
			if nextFlushed != flushed {
				if fresh, err := c.recent(rctx); err == nil {
					recent = fresh
				}
			}
			generation, flushed = nextGeneration, nextFlushed
			if err := send(recent); err != nil {
				return nil
			}
		case <-beat.C:
			beat.Reset(interval)
			if err := sse.Raw([]byte(": ping\n\n")); err != nil {
				return nil
			}
		}
	}
}

func (c *UsageController) generation() uint64 { return c.Inflight.Generation() }

func (c *UsageController) flushed() uint64 {
	if c.Flushed == nil {
		return 0
	}
	return c.Flushed()
}
