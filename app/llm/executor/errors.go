package executor

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"syscall"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/platform/file"
	"rllmx/app/platform/text"
)

// MaxErrorBody bounds how much of an error body is read.
const MaxErrorBody = 64 << 10

const maxMessage = 300

// UpstreamError is a failed answer reduced to what the pipeline may use: the
// status, a short markup-free message for the account's last-error field, and
// the time the provider says its limit ends when it says so.
type UpstreamError struct {
	Status  int
	Message string
	ResetAt time.Time
}

// ReadError reads an error body under the cap and parses it.
func ReadError(status int, body io.Reader) UpstreamError {
	raw := file.ReadUpTo(body, MaxErrorBody)
	return ParseError(status, raw)
}

// ParseError takes the message from the places providers put it, in this order:
// error.message, message, an error string, an error object printed, then the
// raw text. Whatever it finds is stripped of markup and control characters and
// cut short, because the message is stored and shown on the dashboard and an
// HTML page must never reach either.
func ParseError(status int, body []byte) UpstreamError {
	e := UpstreamError{Status: status}
	msg := ""
	if gjson.ValidBytes(body) {
		g := gjson.ParseBytes(body)
		for _, p := range []string{"error.message", "message", "error"} {
			v := g.Get(p)
			if v.Type == gjson.String && v.String() != "" {
				msg = v.String()
				break
			}
		}
		if msg == "" && g.Get("error").IsObject() {
			msg = g.Get("error").Raw
		}
		e.ResetAt = resetAt(g)
	}
	if msg == "" {
		msg = string(body)
	}
	e.Message = text.StripMarkup(msg, maxMessage)
	return e
}

// resetAt reads a provider-reported reset: a Unix time or a number of seconds,
// in the fields the providers this program ships use.
func resetAt(g gjson.Result) time.Time {
	if v := g.Get("error.resets_at"); v.Exists() && v.Int() > 0 {
		return time.Unix(v.Int(), 0)
	}
	if v := g.Get("error.resets_in_seconds"); v.Exists() && v.Int() > 0 {
		return time.Now().Add(time.Duration(v.Int()) * time.Second)
	}
	return time.Time{}
}

// errStaleSocket marks a failure to use a pooled connection the far end dropped
// without telling anyone. The request was never written, so it may be sent once
// more at once; no other failure is replayed, because the upstream may already
// have it.
var errStaleSocket = errors.New("executor: the pooled connection was gone")

// isStaleSocketError reports a connection that died between pooling and use: a
// reset, a broken pipe, a closed or prematurely ended socket. A name that does
// not resolve, a refusal, an unreachable host, a timeout, a certificate the TLS
// check refused and a cancelled request are all something else, so they are left
// to the retry table.
func isStaleSocketError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return false
	}
	var cert *tls.CertificateVerificationError
	if errors.As(err, &cert) {
		return false
	}
	for _, code := range []syscall.Errno{syscall.ECONNRESET, syscall.EPIPE} {
		if errors.Is(err, code) {
			return true
		}
	}
	for _, code := range []syscall.Errno{syscall.ECONNREFUSED, syscall.EHOSTUNREACH, syscall.ENETUNREACH} {
		if errors.Is(err, code) {
			return false
		}
	}
	return errors.Is(err, net.ErrClosed) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}
