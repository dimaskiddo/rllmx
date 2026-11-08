package providers

import "sync"

// lazy builds a value on first use. The stores open in a runner after every
// provider registered, so what needs them cannot be built when the routes are
// declared. A failed build is not kept; the next call retries.
type lazy[T any] struct {
	build func() (*T, error)

	mu sync.Mutex
	v  *T
}

func (l *lazy[T]) get() (*T, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.v != nil {
		return l.v, nil
	}
	v, err := l.build()
	if err != nil {
		return nil, err
	}
	l.v = v
	return v, nil
}
