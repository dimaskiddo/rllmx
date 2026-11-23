package account

import (
	"context"
	"time"

	"github.com/goravel/framework/contracts/log"

	"rllmx/app/store/repository"
)

// ConnWriter is the part of the connection repository the reporter writes to.
type ConnWriter interface {
	LockFromLevel(ctx context.Context, id, model string, status int, reason string, decide func(level int) (until time.Time, next int, lock bool)) (bool, error)
	ClearLocks(ctx context.Context, id, model string, at, since time.Time) error
}

var _ ConnWriter = (*repository.ConnectionsRepo)(nil)

// Reporter records how an upstream answered: a failure locks the model, or the
// whole account, for a time that depends on the error; a success clears it.
type Reporter struct {
	conns ConnWriter
	log   log.Log
	now   func() time.Time
}

// NewReporter returns a Reporter. A nil now means time.Now.
func NewReporter(c ConnWriter, l log.Log, now func() time.Time) *Reporter {
	if now == nil {
		now = time.Now
	}
	return &Reporter{conns: c, log: l, now: now}
}

// FailInput describes one failed upstream attempt.
type FailInput struct {
	ConnID   string
	Provider string
	Model    string
	Status   int
	Text     string
	// ResetAt is when the provider says its limit ends, or the zero time.
	ResetAt time.Time
}

// Outcome is what the pipeline needs after a failure.
type Outcome struct {
	// Fallback means the connection was locked: try another account.
	Fallback bool
	// RequestScoped means the request itself was refused: nothing was locked, but
	// a combo may try its next member.
	RequestScoped bool
}

// Fail locks the connection when its credential is at fault. The backoff level
// is read inside the write, so concurrent failures all count. The error text is
// stored for the dashboard, cut short, and never logged.
func (r *Reporter) Fail(ctx context.Context, in FailInput) (Outcome, error) {
	if in.ConnID == "" {
		return Outcome{}, nil
	}
	now := r.now()
	classify := func(level int) Decision {
		return Classify(Input{Status: in.Status, Text: in.Text, Provider: in.Provider, Level: level, ResetAt: in.ResetAt, Now: now})
	}
	probe := classify(0)
	if !probe.Fallback {
		return Outcome{RequestScoped: probe.RequestScoped}, nil
	}
	model := in.Model
	if probe.AccountWide {
		model = ""
	}
	var d Decision
	_, err := r.conns.LockFromLevel(ctx, in.ConnID, model, in.Status, in.Text, func(level int) (time.Time, int, bool) {
		d = classify(level)
		return now.Add(d.Cooldown), d.NextLevel, true
	})
	if err != nil {
		return Outcome{}, err
	}
	if r.log != nil {
		r.log.Infof("account lock provider=%s model=%s conn=%s status=%d cooldown=%s", in.Provider, in.Model, in.ConnID, in.Status, d.Cooldown)
	}
	return Outcome{Fallback: true}, nil
}

// Succeed clears the locks a success makes obsolete. started is when the request
// began: a failure recorded after it belongs to another request and stays.
func (r *Reporter) Succeed(ctx context.Context, connID, model string, started time.Time) error {
	if connID == "" {
		return nil
	}
	return r.conns.ClearLocks(ctx, connID, model, r.now(), started)
}
