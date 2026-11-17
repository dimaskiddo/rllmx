package redactor

import "github.com/goravel/framework/contracts/log"

// Channel is a custom log driver (config "via") that opens the framework's
// handler for a channel and puts the redactor in front of it. The inner handler
// reads its path, level, format and retention from the same channel path, so
// the channel's keys stay where the framework expects them.
type Channel struct {
	inner log.Logger
	tee   func(log.Entry)
}

// NewChannel wraps a framework log driver such as logger.NewSingle or
// logger.NewDaily.
func NewChannel(inner log.Logger) *Channel {
	return &Channel{inner: inner}
}

// Tee hands every clean entry to fn as well, before the channel writes it.
// Wiring calls this once, before the channel is opened.
func (c *Channel) Tee(fn func(log.Entry)) { c.tee = fn }

// Handle opens the inner handler for channel and wraps it.
func (c *Channel) Handle(channel string) (log.Handler, error) {
	h, err := c.inner.Handle(channel)
	if err != nil {
		return nil, err
	}
	out := NewHandler(h)
	if c.tee != nil {
		out.Tee(c.tee)
	}
	return out, nil
}
