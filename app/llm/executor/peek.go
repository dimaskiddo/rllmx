package executor

import (
	"context"
	"io"
	"time"
)

const (
	// peekLimit is how much of an answer is read looking for an error that came
	// inside a 200.
	peekLimit = 256 << 10
	peekChunk = 32 << 10
)

type readResult struct {
	data []byte
	err  error
}

// peeker reads a body in the background, one read at a time, so a wait for the
// next bytes can end on a timer without losing what the read later returns.
type peeker struct {
	body     io.ReadCloser
	ch       chan readResult
	inflight bool
}

func newPeeker(body io.ReadCloser) *peeker {
	return &peeker{body: body, ch: make(chan readResult, 1)}
}

func (p *peeker) start() {
	if p.inflight {
		return
	}
	p.inflight = true
	go func() {
		// A fresh slice per read: the result is handed to the replay and kept.
		buf := make([]byte, peekChunk)
		n, err := p.body.Read(buf)
		p.ch <- readResult{buf[:n], err}
	}()
}

// next waits for the pending read. timedOut is true when idle passed first; the
// read stays pending and a later call, or the replay, collects it.
func (p *peeker) next(ctx context.Context, idle time.Duration) (res readResult, timedOut bool) {
	p.start()
	var expire <-chan time.Time
	if idle > 0 {
		t := time.NewTimer(idle)
		defer t.Stop()
		expire = t.C
	}
	select {
	case res = <-p.ch:
		p.inflight = false
		return res, false
	case <-expire:
		return readResult{}, true
	case <-ctx.Done():
		return readResult{err: ctx.Err()}, false
	}
}

// replay gives back what the peek read, then the rest of the body.
type replay struct {
	p      *peeker
	prefix []byte
	err    error
}

func (r *replay) Read(b []byte) (int, error) {
	for {
		if len(r.prefix) > 0 {
			n := copy(b, r.prefix)
			r.prefix = r.prefix[n:]
			return n, nil
		}
		if r.err != nil {
			return 0, r.err
		}
		if r.p.inflight {
			res := <-r.p.ch
			r.p.inflight = false
			r.prefix, r.err = res.data, res.err
			continue
		}
		return r.p.body.Read(b)
	}
}

func (r *replay) Close() error { return r.p.body.Close() }

// PeekVerdictFunc looks at everything read so far. It reports a refusal with the
// status it stands for, or that the part worth deciding on has arrived.
type PeekVerdictFunc func(seen []byte) (status int, refused, enough bool)

// PeekUntil reads the start of an answer until check has seen enough, the limit
// is reached, the body ends or goes quiet. A refusal closes the body and returns
// its status; otherwise the returned body gives back every byte read and then
// the rest. seen is what was read, for a caller that wants to look again.
func PeekUntil(ctx context.Context, body io.ReadCloser, idle time.Duration, check PeekVerdictFunc) (status int, rest io.ReadCloser, seen []byte) {
	p := newPeeker(body)
	var readErr error
	for len(seen) < peekLimit {
		res, timedOut := p.next(ctx, idle)
		if timedOut {
			break
		}
		seen = append(seen, res.data...)
		readErr = res.err
		st, refused, enough := check(seen)
		if refused {
			_ = body.Close()
			return st, nil, seen
		}
		if enough || res.err != nil {
			break
		}
	}
	return 0, &replay{p: p, prefix: seen, err: readErr}, seen
}
