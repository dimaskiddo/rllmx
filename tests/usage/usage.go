// Package usage gives tests a place to collect the usage rows a request
// leaves behind. It is a sub-package because it imports app/analytics, whose
// own tests import the root tests package.
package usage

import (
	"sync"

	"rllmx/app/analytics"
)

// Sink collects enqueued rows. It is safe for concurrent use.
type Sink struct {
	mu   sync.Mutex
	list []analytics.Record
}

// Enqueue records a row and reports it as accepted.
func (s *Sink) Enqueue(r analytics.Record) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.list = append(s.list, r)
	return true
}

// All returns a copy of the rows collected so far, in order.
func (s *Sink) All() []analytics.Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]analytics.Record(nil), s.list...)
}
