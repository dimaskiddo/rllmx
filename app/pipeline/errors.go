package pipeline

import (
	"context"
	"errors"
	"net/http"
	"time"

	"rllmx/app/failure"
	"rllmx/app/stream"
)

// statusClientClosed is the status stored for a client that left; nothing is
// written to it.
const statusClientClosed = 499

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
}

func classOf(status int) string {
	switch {
	case status < 400:
		return "none"
	case status == http.StatusUnauthorized:
		return "auth"
	case status == http.StatusForbidden:
		return "access_denied"
	case status == http.StatusPaymentRequired:
		return "quota"
	case status == http.StatusTooManyRequests:
		return "rate_limit"
	case status == http.StatusNotFound:
		return "not_found"
	case status == http.StatusGatewayTimeout:
		return "timeout"
	case status >= 500:
		return "upstream_5xx"
	}
	return "invalid_request"
}

func streamClass(err error) string {
	switch {
	case errors.Is(err, stream.ErrStall):
		return "stream_stall"
	case errors.Is(err, stream.ErrFirstChunk):
		return "timeout"
	case errors.Is(err, stream.ErrClientGone):
		return "client_closed"
	}
	return "upstream_5xx"
}

// clientStatus is the status a client sees for an upstream status. A refused
// credential is the router's problem, not the client's: a 401 would make the
// client discard a good key.
func clientStatus(status int) int {
	if status < 400 || status == http.StatusUnauthorized || status == http.StatusForbidden {
		return http.StatusBadGateway
	}
	return status
}

func (c *call) fail(status int, class, msg string) Output {
	c.errClass = class
	return Output{Status: status, Header: failure.Headers(), Body: failure.Body(c.failureFormat(), status, msg)}
}

func (c *call) closed() Output {
	c.errClass = "client_closed"
	return Output{Status: statusClientClosed}
}

// internal answers a failure of the router's own, with nothing of the cause.
func (c *call) internal(ctx context.Context, err error) Output {
	if ctx.Err() != nil {
		return c.closed()
	}
	if c.p.d.Log != nil {
		c.p.d.Log.Warningf("pipeline: %v", err)
	}
	return c.fail(http.StatusInternalServerError, "unknown", "Internal server error")
}
