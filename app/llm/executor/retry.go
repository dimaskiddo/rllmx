package executor

import (
	"context"
	"io"
	"net/http"
	"time"
)

// defaultDelay is how long a retry rule written without a delay waits.
const defaultDelay = 2 * time.Second

// maxDrain bounds how much of a discarded body is read so the connection can be
// reused.
const maxDrain = 64 << 10

// Rule is one in-place retry: a status gets Attempts more tries, each after
// Delay. A network error follows the rule for 502.
type Rule struct {
	Attempts int
	Delay    time.Duration
}

// defaultRetry is the table every provider starts with. A 429 is not retried in
// place: another account serves it.
var defaultRetry = map[int]Rule{
	http.StatusBadGateway:         {Attempts: 3, Delay: 3 * time.Second},
	http.StatusServiceUnavailable: {Attempts: 3, Delay: 2 * time.Second},
	http.StatusGatewayTimeout:     {Attempts: 2, Delay: 3 * time.Second},
	http.StatusTooManyRequests:    {Attempts: 0},
}

func (r Rule) Backoff() time.Duration {
	if r.Delay <= 0 {
		return defaultDelay
	}
	return r.Delay
}

// Wait sleeps for d or until ctx ends, whichever is first.
func Wait(ctx context.Context, d time.Duration, sleep func(time.Duration) <-chan time.Time) error {
	if sleep == nil {
		t := time.NewTimer(d)
		defer t.Stop()
		sleep = func(time.Duration) <-chan time.Time { return t.C }
	}
	select {
	case <-sleep(d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Drain reads what is left of a body, within a bound, and closes it, so a retry
// does not leave the earlier attempt's connection hanging.
func Drain(rc io.ReadCloser) {
	if rc == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(rc, maxDrain))
	_ = rc.Close()
}

func msDuration(ms int) time.Duration { return time.Duration(ms) * time.Millisecond }
