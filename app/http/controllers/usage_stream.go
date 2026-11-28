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
	// A standard user's feed carries only their own keys, the same narrowing
	// the stats and the log take. A key list that cannot be read is not "every
	// key": the feed refuses before it opens.
	keys, refused, ok := c.scope(ctx)
	if !ok {
		return refused
	}
	var accounts map[string]string
	if keys == nil {
		accounts = accountNames(rctx, c.store())
	}
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

	send := func(recent []usageRecent) error {
		raw, err := json.Marshal(c.feedFrame(keys, accounts, recent))
		if err != nil {
			return err
		}
		return sse.Data(string(raw))
	}

	// The counters are read before the first frame: a change that lands while
	// it is written must still produce the next one.
	generation, flushed := c.generation(), c.flushed()
	recent, err := c.recent(rctx, keys)
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
				if fresh, err := c.recent(rctx, keys); err == nil {
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

// feedFrame is one frame of the feed. The live table names upstream accounts,
// which are the operator's, so a scoped caller's frame carries empty rows where
// the stats answer omits the table.
func (c *UsageController) feedFrame(keys []string, accounts map[string]string, recent []usageRecent) contractshttp.Json {
	if keys != nil {
		return contractshttp.Json{
			"pending": contractshttp.Json{}, "activeRequests": []usageActive{}, "recentRequests": recent,
			"errorProvider": "",
		}
	}
	pending, active := c.live(accounts)
	return contractshttp.Json{
		"pending": pending, "activeRequests": active, "recentRequests": recent,
		"errorProvider": c.Inflight.ErrorProvider(),
	}
}

func (c *UsageController) generation() uint64 { return c.Inflight.Generation() }

func (c *UsageController) flushed() uint64 {
	if c.Flushed == nil {
		return 0
	}
	return c.Flushed()
}
