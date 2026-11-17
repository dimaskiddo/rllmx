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
	// build receives a context that ends after buildTimeout.
	build func(ctx context.Context) (*T, error)

	v  atomic.Pointer[T]
	mu sync.Mutex
}

func (l *lazy[T]) get() (*T, error) {
	if v := l.v.Load(); v != nil {
		return v, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if v := l.v.Load(); v != nil {
		return v, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), buildTimeout)
	defer cancel()
	v, err := l.build(ctx)
	if err != nil {
		return nil, err
	}
	l.v.Store(v)
	return v, nil
}

// reset drops the built value so the next get rebuilds it. The lock orders the
// reset after a build already in flight, so a caller that writes and then warms
// never keeps a table read before the write.
func (l *lazy[T]) reset() {
	l.mu.Lock()
	l.v.Store(nil)
	l.mu.Unlock()
}
