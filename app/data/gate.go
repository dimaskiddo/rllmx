// Package data owns the start-up of the stores: it takes the data lock, opens
// and migrates the configuration database, runs the maintenance jobs, and tells
// the HTTP server when it may serve.
package data

import (
	"context"
	"sync"
)

// Gate is a one-way signal. The data runner decides it once, open or failed,
// and every waiter sees that answer.
type Gate struct {
	once   sync.Once
	done   chan struct{}
	opened bool
}

// NewGate builds a gate nothing has decided. The server waits on it, so a start that never reaches the store blocks rather than serving an empty one.
func NewGate() *Gate { return &Gate{done: make(chan struct{})} }

func (g *Gate) decide(opened bool) {
	g.once.Do(func() {
		g.opened = opened
		close(g.done)
	})
}

// Open lets the waiters through. It does nothing once the gate has been decided.
func (g *Gate) Open() { g.decide(true) }

// Fail refuses the waiters. It does nothing once the gate has been decided, so a
// shutdown after a good start leaves the gate open.
func (g *Gate) Fail() { g.decide(false) }

// Wait blocks until the gate is decided or ctx ends, and reports whether the
// gate opened.
func (g *Gate) Wait(ctx context.Context) bool {
	select {
	case <-g.done:
		return g.opened
	case <-ctx.Done():
		return false
	}
}
