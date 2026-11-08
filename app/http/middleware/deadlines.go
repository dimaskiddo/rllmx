package middleware

import (
	nethttp "net/http"
	"time"

	contractshttp "github.com/goravel/framework/contracts/http"

	apphttp "rllmx/app/http"
	"rllmx/app/stream"
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

// Deadlines sets the socket deadlines once, at entry. A handler must never set
// a read deadline after it read the body: net/http's background read would fail
// and cancel the request context, and the upstream call with it.
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
		_ = rc.SetReadDeadline(now.Add(first(m.APIRead, APIDefaultRead)))
		_ = rc.SetWriteDeadline(now.Add(first(m.APIWrite, APIDefaultWrite)))
	case apphttp.IsClientAPI(r.URL.Path) && r.Body != nil && r.Body != nethttp.NoBody:
		_ = rc.SetReadDeadline(now.Add(first(m.ClientRead, ClientDefaultRead)))
	}
	ctx.Request().Next()
}
