package pipeline

import (
	"context"
	"errors"
	"net/http"
	"time"

	"rllmx/app/llm/stream"
	"rllmx/app/platform/failure"
)

// statusClientClosed is the status stored for a client that left; nothing is
// written to it.
const statusClientClosed = failure.StatusClientClosed

// accountFail is a failure that may be the account's fault. The loop reports it
// and, when the account is locked, tries the next one with the answer in hand.
type accountFail struct {
	status  int
	class   string
	text    string
	resetAt time.Time
	// expired marks an account whose refresh token is dead. The mark is already
	// stored and is the stronger state, so nothing is locked on top of it.
	expired bool
	// gated marks a refusal the quota gate has already accounted for: the
	// account is passed over for this model until the gate's own time, with no
	// lock stored.
	gated bool
	// empty marks an answer with no content: it ends a single-model request as it
	// always did, and is never reported, because no account failed.
	empty bool
	// noReport marks a failure that is nobody's account's fault, so the request
	// moves on without storing a lock or a cooldown: a token service that cannot
	// be reached is down for every account at once.
	noReport bool
}

func streamClass(err error) string {
	switch {
	case errors.Is(err, stream.ErrStall):
		return "stream_stall"
	case errors.Is(err, stream.ErrFirstChunk):
		return "timeout"
	case errors.Is(err, stream.ErrTruncated):
		return "truncated"
	}
	return "upstream_5xx"
}

func (c *call) fail(status int, class, msg string) Output {
	c.errClass = class
	return Output{Status: status, Header: failure.Headers(), Body: failure.Body(c.failureFormat(), status, msg)}
}

func (c *call) closed() Output {
	c.errClass = "client_closed"
	return Output{Status: statusClientClosed}
}

// stopped answers a request whose context ended. Only a cancel is the client
// leaving; a passed deadline belongs to whoever set it — a probe, the router's
// own question, a combo wait — and is a timeout.
func (c *call) stopped(ctx context.Context) Output {
	if status, class := failure.Stopped(ctx.Err()); status == http.StatusGatewayTimeout {
		return c.fail(status, class, "The request ran out of time")
	}
	return c.closed()
}

// internal answers a failure of the router's own, with nothing of the cause.
// refuseModel answers a key that may not call the model it named.
func (c *call) refuseModel() Output {
	return c.fail(http.StatusForbidden, "access_denied", failure.NotAllowed(c.requested))
}

func (c *call) internal(ctx context.Context, err error) Output {
	if ctx.Err() != nil {
		return c.stopped(ctx)
	}
	if c.p.d.Log != nil {
		c.p.d.Log.Warningf("pipeline: %v", err)
	}
	return c.fail(http.StatusInternalServerError, "unknown", "Internal server error")
}
