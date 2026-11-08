package analytics

import (
	"context"
	"fmt"
	"rllmx/app/dialect"
	"time"
)

// Prune deletes requests and hourly rollups older than days. Daily rollups stay:
// they are tiny and long charts need them. A non-positive days keeps everything.
// It returns the number of request rows removed.
func (s *Store) Prune(ctx context.Context, now time.Time, days int) (int64, error) {
	if days <= 0 {
		return 0, nil
	}
	cutoff := now.UTC().AddDate(0, 0, -days).UnixMilli()
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		res, err := s.db.WithContext(ctx).Delete(s.q(`DELETE FROM "requests" WHERE "id" IN (SELECT "id" FROM "requests" WHERE "ts" < ? ORDER BY "ts" LIMIT ?)`), cutoff, s.batch)
		if err != nil {
			return total, err
		}
		n := int64(0)
		if res != nil {
			n = res.RowsAffected
		}
		total += n
		if n < int64(s.batch) {
			break
		}
	}
	_, err := s.db.WithContext(ctx).Delete(s.q(`DELETE FROM "rollupHourly" WHERE "bucket" < ?`), hourBucket(time.UnixMilli(cutoff)))
	return total, err
}

// IncrementalVacuum returns up to pages free pages to the operating system. It
// does something only on a SQLite file created with auto_vacuum=INCREMENTAL.
func (s *Store) IncrementalVacuum(ctx context.Context, pages int) error {
	if s.Dialect() != dialect.SQLite || pages <= 0 {
		return nil
	}
	return s.db.WithContext(ctx).Statement(fmt.Sprintf(`PRAGMA incremental_vacuum(%d)`, pages))
}

// Analyze refreshes PostgreSQL planner statistics on the usage tables.
func (s *Store) Analyze(ctx context.Context) error {
	if s.Dialect() != dialect.Postgres {
		return nil
	}
	for _, t := range []string{"requests", "rollupHourly", "rollupDaily"} {
		if err := s.db.WithContext(ctx).Statement(`ANALYZE "` + t + `"`); err != nil {
			return err
		}
	}
	return nil
}
