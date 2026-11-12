package oauth

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"time"

	"rllmx/app/store/repository"
)

// Timing of the background pass.
const (
	FirstPass = 10 * time.Second
	PassEvery = 5 * time.Minute
)

// due picks the accounts a pass renews: active, readable, not expired, with a
// refresh token and a known expiry within the flow's lead time or half an
// hour, whichever is longer.
func (r *Refresher) pending(list []repository.Connection) []repository.Connection {
	now := r.now()
	var out []repository.Connection
	for _, c := range list {
		if c.AuthType != "oauth" || c.Corrupt || !c.IsActive {
			continue
		}
		d := c.Doc()
		if status, _ := d.GetString("testStatus"); status == repository.StatusExpired {
			continue
		}
		f, ok := r.FlowOf(c)
		if !ok {
			continue
		}
		if rt, _ := d.GetString("refreshToken"); rt == "" {
			continue
		}
		exp, known := timeOf(d, "expiresAt")
		if !known {
			continue
		}
		window := max(time.Duration(f.Refresh.LeadSeconds)*time.Second, backgroundMin)
		if exp.Sub(now) < window {
			out = append(out, c)
		}
	}
	return out
}

// Pass renews every account that is close to expiry, one at a time, pausing
// between them. One account failing never stops the pass; only the class of a
// failure is logged. It returns how many accounts it renewed.
func (r *Refresher) Pass(ctx context.Context, gap time.Duration) int {
	st, err := r.Cooldowns()
	if err != nil {
		return 0
	}
	active := true
	list, err := st.List(ctx, repository.ConnFilter{IsActive: &active})
	if err != nil {
		r.warn("oauth: the background pass could not list accounts")
		return 0
	}
	renewed := 0
	pause := gap
	for i, c := range r.pending(list) {
		if ctx.Err() != nil {
			return renewed
		}
		if i > 0 && pause > 0 {
			if r.wait(ctx, pause) != nil {
				return renewed
			}
		}
		pause = r.gapAfter(c, gap)
		if _, err := r.Refresh(ctx, c.ID, ""); err != nil {
			r.warn("oauth: the background renewal of a %s account failed: %s", c.Provider, reason(err))
			continue
		}
		renewed++
	}
	return renewed
}

// MaxSlowJitter is the most random time added to the pause after a slow
// provider's account, so several of them do not renew on a fixed beat.
const MaxSlowJitter = 4 * time.Second

// gapAfter is the pause before the next account once c has been renewed: the
// common gap, or for a provider that rate-limits renewals the longer SlowGap
// plus some jitter.
func (r *Refresher) gapAfter(c repository.Connection, gap time.Duration) time.Duration {
	f, ok := r.FlowOf(c)
	if !ok || !f.SlowRefresh || r.SlowGap <= 0 {
		return gap
	}
	return r.SlowGap + r.jitter()
}

func (r *Refresher) jitter() time.Duration {
	if r.Jitter != nil {
		return r.Jitter(MaxSlowJitter)
	}
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0
	}
	return time.Duration(uint64(binary.BigEndian.Uint16(b[:])) * uint64(MaxSlowJitter) / 65536)
}

// reason names a failure without provider text.
func reason(err error) string {
	if c := ClassOf(err); c != "" {
		return string(c)
	}
	return err.Error()
}

// Loop runs a pass after FirstPass and then every PassEvery until ctx ends. The
// caller starts it only when the store is open.
func (r *Refresher) Loop(ctx context.Context, gap time.Duration) {
	delay := FirstPass
	for {
		if r.wait(ctx, delay) != nil {
			return
		}
		r.Pass(ctx, gap)
		delay = PassEvery
	}
}
