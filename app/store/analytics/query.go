package analytics

import (
	"context"
	"math"
	"time"
)

// Dims names one row of the usage tables: the provider, the model, the account,
// the client key and the endpoint.
type Dims struct {
	Provider     string
	Model        string
	ConnectionID string
	APIKeyID     string
	Endpoint     string
}

// Sum is one dimension's usage over a window.
type Sum struct {
	Dims
	Requests, Errors                                  int64
	Prompt, Completion, Cached, CacheWrite, Reasoning int64
	Cost                                              float64
	Last                                              time.Time
}

// Bucket is one hour or day on a chart. Tokens are prompt plus completion, as
// the dashboard plots them; Daily marks a day taken from the daily rollups.
type Bucket struct {
	Start    time.Time
	Daily    bool
	Requests int64
	Tokens   int64
	Cost     float64
}

// Request is one usage row, as the log and the recent list read it.
type Request struct {
	Time         time.Time
	Provider     string
	Model        string
	ConnectionID string
	Prompt       int64
	Completion   int64
	Cached       int64
	Status       int64
	ErrorClass   string
}

const (
	// maxRecent bounds the rows one call may ask for.
	maxRecent = 200
	groupCols = `"provider","model","connectionId","apiKeyId","endpoint"`
	sumAgg    = `COALESCE(SUM("requests"),0) AS "requests",COALESCE(SUM("errors"),0) AS "errors",` +
		`COALESCE(SUM("promptTokens"),0) AS "promptTokens",COALESCE(SUM("completionTokens"),0) AS "completionTokens",` +
		`COALESCE(SUM("cachedTokens"),0) AS "cachedTokens",COALESCE(SUM("cacheWriteTokens"),0) AS "cacheWriteTokens",` +
		`COALESCE(SUM("reasoningTokens"),0) AS "reasoningTokens",COALESCE(SUM("cost"),0) AS "cost"`
)

// splitAt is the first hour the hourly rollups still hold, rounded up to the
// next day: the days before it can only come from the daily rollups, which are
// never pruned, and the hours from it on from the hourly ones. A day whose
// hours were half pruned is therefore answered whole, once, from the daily
// table.
func (s *Store) splitAt(ctx context.Context) (int64, error) {
	var min []int64
	if err := s.db.WithContext(ctx).Select(&min, s.q(`SELECT COALESCE(MIN("bucket"),0) FROM "rollupHourly"`)); err != nil {
		return 0, err
	}
	if len(min) == 0 || min[0] == 0 {
		return math.MaxInt64, nil
	}
	return time.UnixMilli(dayBucket(time.UnixMilli(min[0]))).AddDate(0, 0, 1).UnixMilli(), nil
}

// table picks the rollup an hourly window reads.
func table(hourly bool) string {
	if hourly {
		return "rollupHourly"
	}
	return "rollupDaily"
}

// start is where an hourly window begins.
func start(from time.Time) int64 {
	if from.IsZero() {
		return 0
	}
	return hourBucket(from)
}

type sumRow struct {
	Provider     string  `db:"provider"`
	Model        string  `db:"model"`
	ConnectionID string  `db:"connectionId"`
	APIKeyID     string  `db:"apiKeyId"`
	Endpoint     string  `db:"endpoint"`
	Requests     int64   `db:"requests"`
	Errors       int64   `db:"errors"`
	Prompt       int64   `db:"promptTokens"`
	Completion   int64   `db:"completionTokens"`
	Cached       int64   `db:"cachedTokens"`
	CacheWrite   int64   `db:"cacheWriteTokens"`
	Reasoning    int64   `db:"reasoningTokens"`
	Cost         float64 `db:"cost"`
	Last         int64   `db:"last"`
}

// Sums totals the usage per dimension from a moment on. hourly reads only the
// hour buckets, for a window inside the request retention; otherwise the daily
// rollups answer the days before the first kept hour and the hourly ones the
// days from it.
func (s *Store) Sums(ctx context.Context, from time.Time, hourly bool) ([]Sum, error) {
	var split int64
	if !hourly {
		var err error
		if split, err = s.splitAt(ctx); err != nil {
			return nil, err
		}
	}
	hourFrom := start(from)
	if hourFrom < split {
		hourFrom = split
	}
	var rows []sumRow
	query := `SELECT ` + groupCols + `,` + sumAgg + `,MAX("bucket") AS "last" FROM ` + table(true) +
		` WHERE "bucket" >= ? GROUP BY ` + groupCols
	if err := s.db.WithContext(ctx).Select(&rows, s.q(query), hourFrom); err != nil {
		return nil, err
	}
	if !hourly {
		dayFrom := int64(0)
		if !from.IsZero() {
			dayFrom = dayBucket(from)
		}
		var days []sumRow
		query := `SELECT ` + groupCols + `,` + sumAgg + `,MAX("bucket") AS "last" FROM ` + table(false) +
			` WHERE "bucket" >= ? AND "bucket" < ? GROUP BY ` + groupCols
		if err := s.db.WithContext(ctx).Select(&days, s.q(query), dayFrom, split); err != nil {
			return nil, err
		}
		rows = append(rows, days...)
	}
	merged := make(map[Dims]*Sum, len(rows))
	for _, r := range rows {
		k := Dims{r.Provider, r.Model, r.ConnectionID, r.APIKeyID, r.Endpoint}
		v := merged[k]
		if v == nil {
			v = &Sum{Dims: k}
			merged[k] = v
		}
		v.Requests += r.Requests
		v.Errors += r.Errors
		v.Prompt += r.Prompt
		v.Completion += r.Completion
		v.Cached += r.Cached
		v.CacheWrite += r.CacheWrite
		v.Reasoning += r.Reasoning
		v.Cost += r.Cost
		if t := time.UnixMilli(r.Last).UTC(); t.After(v.Last) {
			v.Last = t
		}
	}
	out := make([]Sum, 0, len(merged))
	for _, v := range merged {
		out = append(out, *v)
	}
	return out, nil
}

// Buckets returns the totals of every hour or day from a moment on, in the
// rollups Sums reads. A daily answer holds day buckets (Daily set) and hour
// buckets, which the caller folds into its own days. The caller lays out the
// empty buckets.
func (s *Store) Buckets(ctx context.Context, from time.Time, hourly bool) ([]Bucket, error) {
	const cols = `"bucket",COALESCE(SUM("requests"),0) AS "requests",` +
		`COALESCE(SUM("promptTokens"),0)+COALESCE(SUM("completionTokens"),0) AS "tokens",` +
		`COALESCE(SUM("cost"),0) AS "cost"`
	var split int64
	if !hourly {
		var err error
		if split, err = s.splitAt(ctx); err != nil {
			return nil, err
		}
	}
	hourFrom := start(from)
	if hourFrom < split {
		hourFrom = split
	}
	read := func(table string, daily bool, from, to int64) ([]Bucket, error) {
		var rows []struct {
			Bucket   int64   `db:"bucket"`
			Requests int64   `db:"requests"`
			Tokens   int64   `db:"tokens"`
			Cost     float64 `db:"cost"`
		}
		query := `SELECT ` + cols + ` FROM "` + table + `" WHERE "bucket" >= ? AND "bucket" < ? GROUP BY "bucket" ORDER BY "bucket"`
		if err := s.db.WithContext(ctx).Select(&rows, s.q(query), from, to); err != nil {
			return nil, err
		}
		out := make([]Bucket, 0, len(rows))
		for _, r := range rows {
			out = append(out, Bucket{Start: time.UnixMilli(r.Bucket).UTC(), Daily: daily, Requests: r.Requests, Tokens: r.Tokens, Cost: r.Cost})
		}
		return out, nil
	}
	hours, err := read(table(true), false, hourFrom, math.MaxInt64)
	if err != nil {
		return nil, err
	}
	if hourly {
		return hours, nil
	}
	dayFrom := int64(0)
	if !from.IsZero() {
		dayFrom = dayBucket(from)
	}
	days, err := read(table(false), true, dayFrom, split)
	if err != nil {
		return nil, err
	}
	return append(days, hours...), nil
}

// LastSeen is the newest request time per dimension since a moment, read from
// the request rows so a bucket start can be replaced by the real time.
func (s *Store) LastSeen(ctx context.Context, since time.Time) (map[Dims]time.Time, error) {
	var rows []struct {
		Provider     string `db:"provider"`
		Model        string `db:"model"`
		ConnectionID string `db:"connectionId"`
		APIKeyID     string `db:"apiKeyId"`
		Endpoint     string `db:"endpoint"`
		Last         int64  `db:"last"`
	}
	err := s.db.WithContext(ctx).Select(&rows, s.q(`SELECT `+groupCols+`,MAX("ts") AS "last" FROM "requests"`+
		` WHERE "ts" >= ? GROUP BY `+groupCols), since.UnixMilli())
	if err != nil {
		return nil, err
	}
	out := make(map[Dims]time.Time, len(rows))
	for _, r := range rows {
		out[Dims{r.Provider, r.Model, r.ConnectionID, r.APIKeyID, r.Endpoint}] = time.UnixMilli(r.Last).UTC()
	}
	return out, nil
}

// Recent returns the newest request rows, at most maxRecent.
func (s *Store) Recent(ctx context.Context, limit int) ([]Request, error) {
	if limit <= 0 || limit > maxRecent {
		limit = maxRecent
	}
	var rows []struct {
		TS           int64  `db:"ts"`
		Provider     string `db:"provider"`
		Model        string `db:"model"`
		ConnectionID string `db:"connectionId"`
		Prompt       int64  `db:"promptTokens"`
		Completion   int64  `db:"completionTokens"`
		Cached       int64  `db:"cachedTokens"`
		Status       int64  `db:"status"`
		ErrorClass   string `db:"errorClass"`
	}
	err := s.db.WithContext(ctx).Select(&rows, s.q(`SELECT "ts","provider","model","connectionId","promptTokens",`+
		`"completionTokens","cachedTokens","status","errorClass" FROM "requests" ORDER BY "id" DESC LIMIT ?`), limit)
	if err != nil {
		return nil, err
	}
	out := make([]Request, 0, len(rows))
	for _, r := range rows {
		out = append(out, Request{
			Time: time.UnixMilli(r.TS).UTC(), Provider: r.Provider, Model: r.Model, ConnectionID: r.ConnectionID,
			Prompt: r.Prompt, Completion: r.Completion, Cached: r.Cached, Status: r.Status, ErrorClass: r.ErrorClass,
		})
	}
	return out, nil
}

// Providers lists every provider the usage store has served, including days
// whose request rows are pruned, so a filter keeps working.
func (s *Store) Providers(ctx context.Context) ([]string, error) {
	var out []string
	err := s.db.WithContext(ctx).Select(&out, s.q(`SELECT DISTINCT "provider" FROM "rollupDaily" WHERE "provider" <> '' ORDER BY "provider"`))
	return out, err
}
