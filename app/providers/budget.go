package providers

import (
	"context"
	"errors"
	"time"

	"rllmx/app/access/quota"
	"rllmx/app/store/analytics"
	"rllmx/app/store/data"
)

// budget builds the per-key budget check over the same analytics store and
// writer every usage row goes through, so a check sees what was flushed and what
// is still queued.
func Budget(limits *quota.Limits, runner *data.Runner) *quota.Quota {
	return &quota.Quota{
		Limits: limits,
		Usage: func(ctx context.Context, keyID string, since time.Time) (analytics.Totals, error) {
			s := runner.Analytics()
			if s == nil {
				return analytics.Totals{}, errors.New("providers: the analytics store is not open")
			}
			return s.KeyTotals(ctx, keyID, since)
		},
		Pending: func(keyID string) analytics.Totals {
			w := runner.Writer()
			if w == nil {
				return analytics.Totals{}
			}
			return w.Pending(keyID)
		},
		Now: time.Now,
	}
}
