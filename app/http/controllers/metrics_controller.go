package controllers

import (
	"crypto/subtle"
	"net/http"
	"strings"
	"time"

	contractshttp "github.com/goravel/framework/contracts/http"

	"rllmx/app/access/auth"
	"rllmx/app/gateway/health"
	"rllmx/app/gateway/metrics"
	apphttp "rllmx/app/http"
	"rllmx/app/store/repository"
)

// metricsType is the OpenMetrics 1.0 media type.
const metricsType = "application/openmetrics-text; version=1.0.0; charset=utf-8"

// MetricsController answers GET /metrics: the counters in OpenMetrics text,
// behind a bearer token or an HTTP Basic pair.
type MetricsController struct {
	Reg      *metrics.Registry
	Token    string
	Username string
	Password string
	Inflight *health.Inflight
	DB       func() *repository.Store
	Users    *auth.Users
	// Version and Commit identify the running build.
	Version, Commit string
	Trust           apphttp.Trust
	// Limiter counts refused attempts, so a Basic password cannot be guessed at
	// line rate. nil skips the count.
	Limiter *auth.Limiter
	// Now is the clock the limiter reads.
	Now func() time.Time
}

// Index answers the scrape. It writes the body itself: the answer can hold
// thousands of lines and is streamed rather than built in memory.
func (c *MetricsController) Index(ctx contractshttp.Context) contractshttp.Response {
	r := ctx.Request().Origin()
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	// Its own key: a scrape that succeeds must not clear a password guesser's
	// failures, and a bad scrape must not lock the admin's sign-in.
	key := "metrics:" + auth.Key(c.Trust.ClientAddr(r))
	if c.Limiter != nil {
		if wait := c.Limiter.Locked(key, now()); wait > 0 {
			return tooMany(ctx, wait)
		}
	}
	if !c.allowed(r) {
		if c.Limiter != nil {
			c.Limiter.Fail(key, now())
		}
		if c.Username != "" {
			ctx.Response().Header("WWW-Authenticate", `Basic realm="rllmx metrics"`)
		}
		return flat(ctx, http.StatusUnauthorized, "Unauthorized")
	}
	if c.Limiter != nil {
		c.Limiter.Succeed(key)
	}

	w := ctx.Response().Writer()
	w.Header().Set("Content-Type", metricsType)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	return c.write(w, r)
}

// allowed accepts the configured bearer token or Basic pair. An unset method is
// not accepted, and both halves of a pair are always compared, so the timing
// never says which was wrong.
func (c *MetricsController) allowed(r *http.Request) bool {
	if c.Token != "" {
		if got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok &&
			subtle.ConstantTimeCompare([]byte(got), []byte(c.Token)) == 1 {
			return true
		}
	}
	if c.Username != "" {
		user, pass, ok := r.BasicAuth()
		if ok {
			u := subtle.ConstantTimeCompare([]byte(user), []byte(c.Username)) == 1
			p := subtle.ConstantTimeCompare([]byte(pass), []byte(c.Password)) == 1
			return u && p
		}
	}
	return false
}

// write streams the body. The handler has already written the status, so a
// failed write ends the answer and nothing else can be said.
func (c *MetricsController) write(w http.ResponseWriter, r *http.Request) contractshttp.Response {
	_ = c.Reg.Write(w, c.info(r))
	return nil
}

// info reads the live names and the in-flight table. A failed read leaves that
// family out; the scrape still answers.
func (c *MetricsController) info(r *http.Request) metrics.Info {
	ctx := r.Context()
	out := metrics.Info{Version: c.Version, Commit: c.Commit}
	if c.Inflight != nil {
		for _, a := range c.Inflight.Active() {
			out.Inflight = append(out.Inflight, metrics.Active{
				Provider: a.Provider, Model: a.Model, Connection: a.ConnectionID, Count: a.Count,
			})
		}
	}
	var st *repository.Store
	if c.DB != nil {
		st = c.DB()
	}
	users := map[string]string{}
	if c.Users != nil {
		owners, _ := c.Users.Owners(ctx)
		names := map[string]string{}
		for _, u := range c.Users.All(ctx) {
			names[u.ID] = u.Username
		}
		for id, user := range owners {
			users[id] = names[user]
		}
	}
	for id, k := range keyNames(ctx, st) {
		out.Keys = append(out.Keys, metrics.KeyInfo{ID: id, Name: k.name, User: users[id]})
	}
	if st != nil {
		if rows, err := st.Connections().List(ctx, repository.ConnFilter{}); err == nil {
			for _, row := range rows {
				out.Accounts = append(out.Accounts, metrics.AccountInfo{
					ID: row.ID, Provider: row.Provider, Name: connectionAccount(row),
				})
			}
		}
	}
	return out
}
