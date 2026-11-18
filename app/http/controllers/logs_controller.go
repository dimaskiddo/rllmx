package controllers

import (
	"encoding/json"
	"maps"
	nethttp "net/http"
	"time"

	contractshttp "github.com/goravel/framework/contracts/http"

	"rllmx/app/llm/stream"
	"rllmx/app/platform/failure"
	"rllmx/app/platform/logbook"
)

const (
	// logEvery is how often the stream looks for new lines. A batch is what was
	// written since the last look.
	logEvery = 150 * time.Millisecond
	// logBeat keeps a proxy from closing an idle stream.
	logBeat = 25 * time.Second
)

// LogsController serves the Console Log: the ring the redacting logger already
// filled. It is not a second logging path, so nothing here captures output.
type LogsController struct {
	Book *logbook.Book
	// every and beat are the stream's cadence; zero takes the defaults.
	every, beat time.Duration
}

// List answers GET /api/translator/console-logs.
func (c *LogsController) List(ctx contractshttp.Context) contractshttp.Response {
	ctx.Response().Header("Cache-Control", "no-store")
	// One read, so the lines and the generation they belong to cannot differ.
	logs, generation := c.Book.Snapshot()
	return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{
		"success": true, "logs": logs, "generation": generation,
	})
}

// Clear answers DELETE /api/translator/console-logs.
func (c *LogsController) Clear(ctx contractshttp.Context) contractshttp.Response {
	if c.Book != nil {
		c.Book.Clear()
	}
	return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{"success": true})
}

// Stream answers GET /api/translator/console-logs/stream: the ring on connect,
// a batched frame when lines arrive, a clear frame when the ring is emptied, and
// a comment every 25 s. It has no write deadline; the writer's own timeout
// bounds a client that stopped reading.
func (c *LogsController) Stream(ctx contractshttp.Context) contractshttp.Response {
	rctx := ctx.Request().Origin().Context()
	w := stream.WriterOf(ctx)
	sse, err := stream.NewSSE(w, stream.Options{WriteTimeout: stream.DefaultWriteTimeout})
	if err != nil {
		failure.Write(w, failure.ForPath(ctx.Request().Origin().URL.Path), nethttp.StatusInternalServerError, "The feed cannot be started")
		return nil
	}
	lines, gen := c.snapshot()
	if err := c.frame(sse, "init", contractshttp.Json{"logs": lines}); err != nil {
		return nil
	}
	seq := int64(0)
	if n := len(lines); n > 0 {
		seq = lines[n-1].Seq
	}
	every, interval := c.every, c.beat
	if every <= 0 {
		every = logEvery
	}
	if interval <= 0 {
		interval = logBeat
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
			fresh, g := c.since(seq, gen)
			if g != gen {
				if err := c.frame(sse, "clear", nil); err != nil {
					return nil
				}
				gen = g
			}
			if len(fresh) == 0 {
				continue
			}
			if err := c.frame(sse, "lines", contractshttp.Json{"lines": fresh}); err != nil {
				return nil
			}
			seq = fresh[len(fresh)-1].Seq
		case <-beat.C:
			beat.Reset(interval)
			if err := sse.Raw([]byte(": ping\n\n")); err != nil {
				return nil
			}
		}
	}
}

func (c *LogsController) snapshot() ([]logbook.Entry, int64) {
	if c.Book == nil {
		return []logbook.Entry{}, 0
	}
	return c.Book.Snapshot()
}

func (c *LogsController) since(seq, gen int64) ([]logbook.Entry, int64) {
	if c.Book == nil {
		return []logbook.Entry{}, gen
	}
	return c.Book.Since(seq, gen)
}

// frame writes one typed frame. The client switches on type, so a clear and a
// batch need no separate event name.
func (c *LogsController) frame(sse *stream.SSE, typ string, fields contractshttp.Json) error {
	body := contractshttp.Json{"type": typ}
	maps.Copy(body, fields)
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	return sse.Data(string(raw))
}
