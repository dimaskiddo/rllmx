package stream

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"
)

// Why a stream stopped. A caller tells them apart to pick a status: a stall is
// the only 504.
var (
	ErrFirstChunk = errors.New("stream: the upstream sent nothing in time")
	ErrStall      = errors.New("stream: the upstream stopped sending data")
	ErrClientGone = errors.New("stream: the client went away")
	ErrUpstream   = errors.New("stream: the upstream stream failed")
	ErrTruncated  = errors.New("stream: the upstream ended before it finished")
	errUsageWait  = errors.New("stream: the usage report did not arrive in time")
	errKeepalive  = errors.New("stream: nothing to send yet")
)

// Timers are the three waits that bound a stream. A zero value disables one.
type Timers struct {
	// FirstChunk bounds the wait for the first upstream event.
	FirstChunk time.Duration
	// Stall bounds the silence between two upstream events.
	Stall time.Duration
	// UsageWait bounds the wait for a usage report once the finish is known.
	UsageWait time.Duration
}

type item struct {
	ev  Event
	err error
}

// pull reads an upstream in the background so the caller can wait on the
// events, the timers and the client at once.
type pull struct {
	ch    chan item
	done  chan struct{}
	once  sync.Once
	src   io.Closer
	t     Timers
	timer *time.Timer
	got   bool
	// deadline is when the running wait runs out. It survives a call that returns
	// for a keepalive, because a comment we sent is not data the upstream sent.
	deadline time.Time
}

func newPull(body io.ReadCloser, src Source, t Timers) *pull {
	p := &pull{ch: make(chan item, 1), done: make(chan struct{}), src: body, t: t}
	go func() {
		for {
			ev, err := src.Next()
			select {
			case p.ch <- item{ev, err}:
			case <-p.done:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	return p
}

// stop releases the reader and closes the upstream, which cancels its request.
func (p *pull) stop() {
	p.once.Do(func() {
		close(p.done)
		_ = p.src.Close()
		if p.timer != nil {
			p.timer.Stop()
		}
	})
}

// next waits for one event. extra, when set, is a deadline for the usage report
// that overrides the stall wait.
func (p *pull) next(ctx context.Context, extra, keep <-chan time.Time) (Event, error) {
	var limit time.Duration
	cause := ErrStall
	if !p.got {
		limit, cause = p.t.FirstChunk, ErrFirstChunk
	} else {
		limit = p.t.Stall
	}
	var fire <-chan time.Time
	if limit > 0 {
		if p.deadline.IsZero() {
			p.deadline = time.Now().Add(limit)
		}
		wait := max(time.Until(p.deadline), 0)
		if p.timer == nil {
			p.timer = time.NewTimer(wait)
		} else {
			if !p.timer.Stop() {
				select {
				case <-p.timer.C:
				default:
				}
			}
			p.timer.Reset(wait)
		}
		fire = p.timer.C
	}
	select {
	case it := <-p.ch:
		if it.err == nil {
			p.got = true
			p.deadline = time.Time{}
		}
		return it.ev, it.err
	case <-ctx.Done():
		return Event{}, ErrClientGone
	case <-fire:
		return Event{}, cause
	case <-extra:
		return Event{}, errUsageWait
	case <-keep:
		return Event{}, errKeepalive
	}
}
