package analytics

import (
	"context"
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

// KeyTotals sums one key's daily rollups since a moment; the zero time means
// every day ever recorded. Rollups count every inserted row, so a refused or
// failed request is in "errors" and is taken back out: a request the provider
// never served does not spend the caller's next one.
func (s *Store) KeyTotals(ctx context.Context, keyID string, since time.Time) (Totals, error) {
	var rows []struct {
		Requests int64   `db:"requests"`
		Tokens   int64   `db:"tokens"`
		Cost     float64 `db:"cost"`
	}
	query := `SELECT COALESCE(SUM("requests"),0)-COALESCE(SUM("errors"),0) AS "requests",` +
		` COALESCE(SUM("promptTokens"),0)+COALESCE(SUM("completionTokens"),0) AS "tokens",` +
		` COALESCE(SUM("cost"),0) AS "cost" FROM "rollupDaily" WHERE "apiKeyId" = ?`
	args := []any{dialect.StripNUL(keyID)}
	if !since.IsZero() {
		query += ` AND "bucket" >= ?`
		args = append(args, dayBucket(since))
	}
	if err := s.db.WithContext(ctx).Select(&rows, s.q(query), args...); err != nil {
		return Totals{}, err
	}
	if len(rows) == 0 {
		return Totals{}, nil
	}
	return Totals{Requests: rows[0].Requests, Tokens: rows[0].Tokens, Cost: rows[0].Cost}, nil
}
