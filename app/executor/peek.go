package executor

import (
	"bytes"
	"context"
	"io"
	"strings"
	"time"
)

const (
	// peekLimit is how much of an answer is read looking for an error that came
	// inside a 200.
	peekLimit = 256 << 10
	peekChunk = 32 << 10
)

// Strings that mark an error event inside a stream. A retry one means the
// upstream is busy and the same account may be tried again; a capacity one means
// this model is full for everybody, so another account should serve.
var (
	peekRetry    = patterns("server_is_overloaded", "service_unavailable_error")
	peekCapacity = patterns("selected model is at capacity", "model_at_capacity")
	// peekOutput marks real output: from here on nothing is retried.
	peekOutput = patterns(
		"event: response.output_text.delta", "event: response.function_call_arguments.delta",
		`"type":"response.output_text.delta"`, `"type":"response.function_call_arguments.delta"`,
	)
)

// patterns returns the lower-case byte form the scan searches for.
func patterns(s ...string) [][]byte {
	out := make([][]byte, len(s))
	for i, v := range s {
		out[i] = []byte(strings.ToLower(v))
	}
	return out
}

type peekVerdict int

const (
	peekClean peekVerdict = iota
	peekBusy
	peekFull
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

// peekStream reads the start of a stream answer. A busy or full verdict means
// the body is closed and nothing is to be replayed; a clean one returns a body
// that yields every byte again.
func peekStream(ctx context.Context, body io.ReadCloser, idle time.Duration) (peekVerdict, io.ReadCloser) {
	p := newPeeker(body)
	var seen []byte
	var scan peekScan
	var readErr error
	for len(seen) < peekLimit {
		res, timedOut := p.next(ctx, idle)
		if timedOut {
			break
		}
		seen = append(seen, res.data...)
		readErr = res.err
		verdict, output := scan.add(res.data)
		if verdict != peekClean {
			_ = body.Close()
			return verdict, nil
		}
		if res.err != nil || output {
			break
		}
	}
	return peekClean, &replay{p: p, prefix: seen, err: readErr}
}

// peekScan looks for an error event that comes before any output, lowercasing
// only the bytes it has not seen. A string that straddles two reads is found
// because each search starts a pattern's length before the new bytes.
type peekScan struct {
	low []byte
}

// longestPeek is the longest string add searches for.
var longestPeek = func() int {
	n := 0
	for _, set := range [][][]byte{peekRetry, peekCapacity, peekOutput} {
		for _, s := range set {
			n = max(n, len(s))
		}
	}
	return n
}()

// add takes the next bytes and reports a busy or full verdict, or that real
// output has started. Once a scan reports output or a verdict the caller stops.
func (s *peekScan) add(chunk []byte) (verdict peekVerdict, output bool) {
	from := max(len(s.low)-longestPeek+1, 0)
	for _, c := range chunk {
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		s.low = append(s.low, c)
	}
	window := s.low[from:]
	out := firstIndex(window, peekOutput)
	if i := firstIndex(window, peekCapacity); i >= 0 && (out < 0 || i < out) {
		return peekFull, false
	}
	if i := firstIndex(window, peekRetry); i >= 0 && (out < 0 || i < out) {
		return peekBusy, false
	}
	return peekClean, out >= 0
}

// firstIndex is the lowest position of any pattern in b, or -1.
func firstIndex(b []byte, patterns [][]byte) int {
	best := -1
	for _, p := range patterns {
		if i := bytes.Index(b, p); i >= 0 && (best < 0 || i < best) {
			best = i
		}
	}
	return best
}
