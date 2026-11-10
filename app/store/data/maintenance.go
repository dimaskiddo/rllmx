package data

import (
	"context"
	"time"

	"rllmx/app/store/analytics"
	"rllmx/app/store/repository"
)

const (
	// sixHours is the upkeep schedule: optimize and checkpoint every six hours.
	sixHours = "0 */6 * * *"
	// daily is the retention schedule: prune, return pages, refresh statistics.
	daily = "0 3 * * *"
)

// MainJobs is the upkeep of the configuration database. The main file's
// auto_vacuum is never touched; a full vacuum stays the explicit db:vacuum
// command. Each job starts a little after the hour, the second one later than
// the first, because the Schedule facade has no jitter. The jobs are declared
// before the store is open, so they ask for it when they run; with no store yet
// they do nothing.
func MainJobs(store func() *repository.Store) []Job {
	on := func(do func(context.Context, *repository.Store) error) func(context.Context) error {
		return func(ctx context.Context) error {
			s := store()
			if s == nil {
				return nil
			}
			return do(ctx, s)
		}
	}
	return []Job{
		{Name: "optimize", Cron: sixHours, Offset: 7 * time.Minute, Run: on(func(ctx context.Context, s *repository.Store) error {
			return s.Optimize(ctx)
		})},
		{Name: "checkpoint", Cron: sixHours, Offset: 19 * time.Minute, Run: on(func(ctx context.Context, s *repository.Store) error {
			return s.Checkpoint(ctx)
		})},
	}
}

// AnalyticsJobs is the upkeep of the usage store: the same optimize and
// checkpoint on its own file, and the daily retention pass. The pruned rows
// come from Options.RetentionDays, which is the setting the API edits.
func AnalyticsJobs(store func() *analytics.Store, retention func() int) []Job {
	on := func(do func(context.Context, *analytics.Store) error) func(context.Context) error {
		return func(ctx context.Context) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			s := store()
			if s == nil {
				return nil
			}
			return do(ctx, s)
		}
	}
	days := func() int {
		if retention == nil {
			return 0
		}
		return retention()
	}
	return []Job{
		{Name: "analytics-optimize", Cron: sixHours, Offset: 11 * time.Minute, Run: on(func(ctx context.Context, s *analytics.Store) error {
			return s.Optimize(ctx)
		})},
		{Name: "analytics-checkpoint", Cron: sixHours, Offset: 23 * time.Minute, Run: on(func(ctx context.Context, s *analytics.Store) error {
			return s.Checkpoint(ctx)
		})},
		{Name: "analytics-prune", Cron: daily, Offset: 9 * time.Minute, Run: on(func(ctx context.Context, s *analytics.Store) error {
			if _, err := s.Prune(ctx, time.Now(), days()); err != nil {
				return err
			}
			return s.IncrementalVacuum(ctx, analytics.VacuumPages)
		})},
		{Name: "analytics-analyze", Cron: daily, Offset: 31 * time.Minute, Run: on(func(ctx context.Context, s *analytics.Store) error {
			return s.Analyze(ctx)
		})},
	}
}
