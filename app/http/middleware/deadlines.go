package middleware

import (
	"io"
	nethttp "net/http"
	"time"

	contractshttp "github.com/goravel/framework/contracts/http"

	apphttp "rllmx/app/http"
	"rllmx/app/llm/stream"
)

const (
	// APIDefaultRead bounds how long a dashboard API call may take to send its
	// body, and APIDefaultWrite the whole answer, which is always small.
	APIDefaultRead  = 30 * time.Second
	APIDefaultWrite = 60 * time.Second
	// ClientDefaultRead bounds an upload to the client API, which carries images
	// and audio. A client API answer has no write deadline: a stream runs for
	// minutes.
	ClientDefaultRead = 10 * time.Minute
)

// Deadlines sets the socket deadlines once, at entry. A read deadline exists to
// bound the upload of a body, so it is set only for a request that has one and
// is lifted when the body ends: net/http watches the socket for a hangup while
// the handler runs, and a deadline still armed there fails that read and cancels
// the request context, and the upstream call or the stream with it.
type Deadlines struct {
	// APIRead, APIWrite and ClientRead override the defaults when non-zero.
	APIRead    time.Duration
	APIWrite   time.Duration
	ClientRead time.Duration
}

// Signature is how the stack names this middleware.
func (m *Deadlines) Signature() string {
	return "Deadlines"
}

func first(d, fallback time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return fallback
}

// Handle sets the deadline for the path. A writer that cannot carry one, as in
// a test recorder, is left without: the request is still correct.
func (m *Deadlines) Handle(ctx contractshttp.Context) {
	r := ctx.Request().Origin()
	rc := nethttp.NewResponseController(stream.WriterOf(ctx))
	now := time.Now()

	switch {
	case apphttp.IsDashboardAPI(r.URL.Path):
		boundRead(rc, r, now.Add(first(m.APIRead, APIDefaultRead)))
		// A feed and the provider tests run longer than any dashboard answer;
		// each of those handlers bounds its own work.
		if !apphttp.IsLongDashboardCall(r.URL.Path) {
			_ = rc.SetWriteDeadline(now.Add(first(m.APIWrite, APIDefaultWrite)))
		}
	case apphttp.IsClientAPI(r.URL.Path) && r.Body != nil && r.Body != nethttp.NoBody:
		boundRead(rc, r, now.Add(first(m.ClientRead, ClientDefaultRead)))
	}
	ctx.Request().Next()
}

// boundRead arms the read deadline for a request with a body and lifts it when
// the body is read to its end or closed. A request without a body has nothing
// to bound.
func boundRead(rc *nethttp.ResponseController, r *nethttp.Request, at time.Time) {
	if r.Body == nil || r.Body == nethttp.NoBody {
		return
	}
	if rc.SetReadDeadline(at) != nil {
		return
	}
	r.Body = &liftOnEnd{ReadCloser: r.Body, lift: func() { _ = rc.SetReadDeadline(time.Time{}) }}
}

// liftOnEnd calls lift once, when the body ends or is closed.
type liftOnEnd struct {
	io.ReadCloser
	lift func()
	done bool
}

func (b *liftOnEnd) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.release()
	}
	return n, err
}

func (b *liftOnEnd) Close() error {
	b.release()
	return b.ReadCloser.Close()
}

func (b *liftOnEnd) release() {
	if !b.done {
		b.done = true
		b.lift()
	}
}
