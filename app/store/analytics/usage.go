package analytics

import (
	"context"
	"math"
	"time"

	"rllmx/app/store/dialect"
)

// Totals is one key's usage in a window: the requests a provider served, the
// tokens those requests used, and what they cost.
type Totals struct {
	Requests int64
	Tokens   int64
	Cost     float64
}

// KeyTotals sums one key's usage since a moment; the zero time means everything
// ever recorded. The daily rollups are cut at UTC midnight, so a window that
// begins elsewhere reads the hourly rollups from its first hour and the daily
// ones only for the days the hourly ones no longer hold. A window that starts
// inside an hour counts that whole hour. Rollups count every inserted row, so a
// refused or failed request is in "errors" and is taken back out: a request the
// provider never served does not spend the caller's next one.
func (s *Store) KeyTotals(ctx context.Context, keyID string, since time.Time) (Totals, error) {
	sp, err := s.spanOf(ctx, since, false)
	if err != nil {
		return Totals{}, err
	}
	id := dialect.StripNUL(keyID)
	hours, err := s.keyTotalsIn(ctx, "rollupHourly", id, sp.hourFrom, math.MaxInt64)
	if err != nil {
		return Totals{}, err
	}
	days, err := s.keyTotalsIn(ctx, "rollupDaily", id, sp.dayFrom, sp.split)
	if err != nil {
		return Totals{}, err
	}
	return Totals{Requests: hours.Requests + days.Requests, Tokens: hours.Tokens + days.Tokens, Cost: hours.Cost + days.Cost}, nil
}

// keyTotalsIn sums one key's rows of a rollup table over a bucket range.
func (s *Store) keyTotalsIn(ctx context.Context, table, keyID string, from, to int64) (Totals, error) {
	var rows []struct {
		Requests int64   `db:"requests"`
		Tokens   int64   `db:"tokens"`
		Cost     float64 `db:"cost"`
	}
	query := `SELECT COALESCE(SUM("requests"),0)-COALESCE(SUM("errors"),0) AS "requests",` +
		` COALESCE(SUM("promptTokens"),0)+COALESCE(SUM("completionTokens"),0) AS "tokens",` +
		` COALESCE(SUM("cost"),0) AS "cost" FROM "` + table + `" WHERE "apiKeyId" = ? AND "bucket" >= ? AND "bucket" < ?`
	if err := s.db.WithContext(ctx).Select(&rows, s.q(query), keyID, from, to); err != nil {
		return Totals{}, err
	}
	if len(rows) == 0 {
		return Totals{}, nil
	}
	return Totals{Requests: rows[0].Requests, Tokens: rows[0].Tokens, Cost: rows[0].Cost}, nil
}
