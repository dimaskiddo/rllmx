package redactor

import (
	"errors"

	"github.com/goravel/framework/contracts/log"
)

// Channel is a custom log driver (config "via") that opens the framework's
// handlers for a channel and puts the redactor in front of them. Each inner
// handler reads its path, level, format and retention from the same channel
// path, so the channel's keys stay where the framework expects them.
type Channel struct {
	inners []log.Logger
	tee    func(log.Entry)
}

// NewChannel wraps the framework log drivers a channel writes to.
func NewChannel(inners ...log.Logger) *Channel {
	return &Channel{inners: inners}
}

// Tee hands every clean entry to fn as well, before the channels write it.
// Wiring calls this once, before the channel is opened.
func (c *Channel) Tee(fn func(log.Entry)) { c.tee = fn }

// Handle opens the inner handlers for channel and wraps them.
func (c *Channel) Handle(channel string) (log.Handler, error) {
	open := make(multi, 0, len(c.inners))
	for _, inner := range c.inners {
		h, err := inner.Handle(channel)
		if err != nil {
			return nil, err
		}
		open = append(open, h)
	}
	out := NewHandler(open)
	if c.tee != nil {
		out.Tee(c.tee)
	}
	return out, nil
}

// multi writes one clean entry to every open channel. A channel whose own level
// drops the entry is skipped, and a failure in one does not stop the rest.
type multi []log.Handler

func (m multi) Enabled(l log.Level) bool {
	for _, h := range m {
		if h.Enabled(l) {
			return true
		}
	}
	return false
}

func (m multi) Handle(e log.Entry) error {
	var err error
	for _, h := range m {
		if h.Enabled(e.Level()) {
			err = errors.Join(err, h.Handle(e))
		}
	}
	return err
}
