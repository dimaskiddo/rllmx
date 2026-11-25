package middleware

import (
	"github.com/goravel/framework/contracts/foundation/configuration"
	"github.com/goravel/framework/contracts/log"

	apphttp "rllmx/app/http"
)

// Options is what the stack needs from the application.
type Options struct {
	Guard *apphttp.Guard
	Trust apphttp.Trust
	Log   log.Log
	// MaxBody is the client API body cap in bytes.
	MaxBody int64
	// LoginPolicy is the sign-in page's CSP when a CAPTCHA provider is set.
	LoginPolicy string
}

// Stack installs the global middleware, outermost first: request id, access log,
// security headers, CORS, deadlines, body limit, cross-site guard, access guard.
// It replaces the driver's stock set (timeout, CORS, TLS redirect, maintenance
// mode) instead of adding to it: the timeout middleware buffers a whole response
// and would cut a stream, and the CORS one reads a config block nothing writes.
func Stack(m configuration.Middleware, o Options) {
	m.Use(
		&RequestID{},
		&AccessLog{Log: o.Log},
		&SecurityHeaders{Trust: o.Trust, LoginPolicy: o.LoginPolicy},
		&CORS{},
		&Deadlines{},
		&BodyLimit{ClientMax: o.MaxBody},
		&SameOrigin{Exempt: []string{samlACS}},
		&Guard{Guard: o.Guard},
	)
	m.Recover(Recover(o.Log))
}
