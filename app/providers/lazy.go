package providers

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// buildTimeout bounds one build, which reads the stores and belongs to no
// request.
const buildTimeout = 30 * time.Second

// lazy builds a value on first use. The stores open in a runner after every
// provider registered, so what needs them cannot be built when the routes are
// declared. A failed build is not kept; the next call retries. Once built, a
// call is one atomic load.
type lazy[T any] struct {
	// build receives a context that ends after buildTimeout. It may return a
	// value together with an error: the caller gets both and the value is not
	// kept, so a stand-in serves until the next call builds again.
	build func(ctx context.Context) (*T, error)

	v atomic.Pointer[T]

	mu sync.Mutex // guards flight and gen only; never held across a build
	// flight is the build running now. Callers that arrive during it wait for
	// its answer, so a failing store costs one attempt, not one per request.
	flight *flight[T]
	gen    uint64
}

type flight[T any] struct {
	done chan struct{}
	v    *T
	err  error
}

func (l *lazy[T]) get() (*T, error) {
	if v := l.v.Load(); v != nil {
		return v, nil
	}
	l.mu.Lock()
	if v := l.v.Load(); v != nil {
		l.mu.Unlock()
		return v, nil
	}
	f := l.flight
	if f == nil {
		f = &flight[T]{done: make(chan struct{})}
		l.flight = f
		go l.run(f, l.gen)
	}
	l.mu.Unlock()
	<-f.done
	return f.v, f.err
}

// run builds on a context of its own: the build reads stores for every caller
// that is waiting, so no one request's deadline or departure may end it.
func (l *lazy[T]) run(f *flight[T], gen uint64) {
	ctx, cancel := context.WithTimeout(context.Background(), buildTimeout)
	defer cancel()
	v, err := l.build(ctx)
	l.mu.Lock()
	// A reset after this build began means what it read may be older than the
	// write that asked for the reset: it answers its waiters and is not kept.
	if err == nil && v != nil && gen == l.gen {
		l.v.Store(v)
	}
	if l.flight == f {
		l.flight = nil
	}
	l.mu.Unlock()
	f.v, f.err = v, err
	close(f.done)
}

// reset drops the built value so the next get rebuilds it. A build already in
// flight keeps its waiters but can no longer store its result, so a caller that
// writes and then warms never keeps a table read before the write.
func (l *lazy[T]) reset() {
	l.mu.Lock()
	l.gen++
	l.flight = nil
	l.v.Store(nil)
	l.mu.Unlock()
}
