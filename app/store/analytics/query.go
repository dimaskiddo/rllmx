package analytics

import (
	"context"
	"math"
	"strings"
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
	APIKeyName   string
	Prompt       int64
	Completion   int64
	Cached       int64
	Status       int64
	ErrorClass   string
	Savers       string
	Skills       string
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

// span is where each rollup is read from for a window that starts at one
// moment: the hourly rollups from hourFrom, and, unless the window is hourly
// only, the daily ones from dayFrom up to split.
type span struct {
	hourFrom, dayFrom, split int64
}

// spanOf lays a window out over the two rollups.
func (s *Store) spanOf(ctx context.Context, from time.Time, hourly bool) (span, error) {
	var sp span
	if !hourly {
		var err error
		if sp.split, err = s.splitAt(ctx); err != nil {
			return span{}, err
		}
		if !from.IsZero() {
			sp.dayFrom = dayBucket(from)
		}
	}
	sp.hourFrom = max(start(from), sp.split)
	return sp, nil
}

// table picks the rollup an hourly window reads.
func table(hourly bool) string {
	if hourly {
		return "rollupHourly"
	}
	return "rollupDaily"
}

// quoted is the rollup table as a query names it. PostgreSQL folds a bare name
// to lower case, and the tables are created with a capital letter.
func quoted(hourly bool) string { return `"` + table(hourly) + `"` }

// start is where an hourly window begins.
func start(from time.Time) int64 {
	if from.IsZero() {
		return 0
	}
	return hourBucket(from)
}

// dimsRow is the five columns a usage row is grouped by.
type dimsRow struct {
	Provider     string `db:"provider"`
	Model        string `db:"model"`
	ConnectionID string `db:"connectionId"`
	APIKeyID     string `db:"apiKeyId"`
	Endpoint     string `db:"endpoint"`
}

func (r dimsRow) dims() Dims { return Dims(r) }

type sumRow struct {
	dimsRow
	Requests   int64   `db:"requests"`
	Errors     int64   `db:"errors"`
	Prompt     int64   `db:"promptTokens"`
	Completion int64   `db:"completionTokens"`
	Cached     int64   `db:"cachedTokens"`
	CacheWrite int64   `db:"cacheWriteTokens"`
	Reasoning  int64   `db:"reasoningTokens"`
	Cost       float64 `db:"cost"`
	Last       int64   `db:"last"`
}

// keyClause narrows a query to the given API keys. A nil list means every key;
// an empty one means none, so a user with no keys reads zero rather than all.
func keyClause(keys []string) (string, []any, bool) {
	if keys == nil {
		return "", nil, true
	}
	if len(keys) == 0 {
		return "", nil, false
	}
	args := make([]any, len(keys))
	for i, k := range keys {
		args[i] = k
	}
	return ` AND "apiKeyId" IN (` + strings.TrimSuffix(strings.Repeat("?,", len(keys)), ",") + `)`, args, true
}

// Sums totals the usage per dimension from a moment on. hourly reads only the
// hour buckets, for a window inside the request retention; otherwise the daily
// rollups answer the days before the first kept hour and the hourly ones the
// days from it. keys narrows the answer, nil meaning every key.
func (s *Store) Sums(ctx context.Context, from time.Time, hourly bool, keys []string) ([]Sum, error) {
	sp, err := s.spanOf(ctx, from, hourly)
	if err != nil {
		return nil, err
	}
	clause, args, usable := keyClause(keys)
	if !usable {
		return []Sum{}, nil
	}
	var rows []sumRow
	query := `SELECT ` + groupCols + `,` + sumAgg + `,MAX("bucket") AS "last" FROM ` + quoted(true) +
		` WHERE "bucket" >= ?` + clause + ` GROUP BY ` + groupCols
	if err := s.db.WithContext(ctx).Select(&rows, s.q(query), append([]any{sp.hourFrom}, args...)...); err != nil {
		return nil, err
	}
	if !hourly {
		var days []sumRow
		query := `SELECT ` + groupCols + `,` + sumAgg + `,MAX("bucket") AS "last" FROM ` + quoted(false) +
			` WHERE "bucket" >= ? AND "bucket" < ?` + clause + ` GROUP BY ` + groupCols
		if err := s.db.WithContext(ctx).Select(&days, s.q(query), append([]any{sp.dayFrom, sp.split}, args...)...); err != nil {
			return nil, err
		}
		rows = append(rows, days...)
	}
	merged := make(map[Dims]*Sum, len(rows))
	for _, r := range rows {
		k := r.dims()
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
func (s *Store) Buckets(ctx context.Context, from time.Time, hourly bool, keys []string) ([]Bucket, error) {
	const cols = `"bucket",COALESCE(SUM("requests"),0) AS "requests",` +
		`COALESCE(SUM("promptTokens"),0)+COALESCE(SUM("completionTokens"),0) AS "tokens",` +
		`COALESCE(SUM("cost"),0) AS "cost"`
	sp, err := s.spanOf(ctx, from, hourly)
	if err != nil {
		return nil, err
	}
	clause, args, usable := keyClause(keys)
	if !usable {
		return []Bucket{}, nil
	}
	read := func(table string, daily bool, from, to int64) ([]Bucket, error) {
		var rows []struct {
			Bucket   int64   `db:"bucket"`
			Requests int64   `db:"requests"`
			Tokens   int64   `db:"tokens"`
			Cost     float64 `db:"cost"`
		}
		query := `SELECT ` + cols + ` FROM "` + table + `" WHERE "bucket" >= ? AND "bucket" < ?` + clause + ` GROUP BY "bucket" ORDER BY "bucket"`
		if err := s.db.WithContext(ctx).Select(&rows, s.q(query), append([]any{from, to}, args...)...); err != nil {
			return nil, err
		}
		out := make([]Bucket, 0, len(rows))
		for _, r := range rows {
			out = append(out, Bucket{Start: time.UnixMilli(r.Bucket).UTC(), Daily: daily, Requests: r.Requests, Tokens: r.Tokens, Cost: r.Cost})
		}
		return out, nil
	}
	hours, err := read(table(true), false, sp.hourFrom, math.MaxInt64)
	if err != nil {
		return nil, err
	}
	if hourly {
		return hours, nil
	}
	days, err := read(table(false), true, sp.dayFrom, sp.split)
	if err != nil {
		return nil, err
	}
	return append(days, hours...), nil
}

// LastSeen is the newest request time per dimension since a moment, read from
// the request rows so a bucket start can be replaced by the real time.
func (s *Store) LastSeen(ctx context.Context, since time.Time, keys []string) (map[Dims]time.Time, error) {
	clause, args, usable := keyClause(keys)
	if !usable {
		return map[Dims]time.Time{}, nil
	}
	var rows []struct {
		dimsRow
		Last int64 `db:"last"`
	}
	err := s.db.WithContext(ctx).Select(&rows, s.q(`SELECT `+groupCols+`,MAX("ts") AS "last" FROM "requests"`+
		` WHERE "ts" >= ?`+clause+` GROUP BY `+groupCols), append([]any{since.UnixMilli()}, args...)...)
	if err != nil {
		return nil, err
	}
	out := make(map[Dims]time.Time, len(rows))
	for _, r := range rows {
		out[r.dims()] = time.UnixMilli(r.Last).UTC()
	}
	return out, nil
}

// Recent returns the newest request rows at or after from, at most limit. A
// zero from keeps every row the store holds. Newest is by time, not by id: an
// import gives old history higher ids than the live rows around it.
func (s *Store) Recent(ctx context.Context, from time.Time, limit int, keys []string) ([]Request, error) {
	return s.recent(ctx, from, limit, "", keys)
}

// RecentServed returns the newest rows that used tokens, at most limit, for a
// list that would otherwise read many rows to keep a few.
func (s *Store) RecentServed(ctx context.Context, limit int, keys []string) ([]Request, error) {
	return s.recent(ctx, time.Time{}, limit, `"promptTokens" + "completionTokens" > 0`, keys)
}

// recent runs the one query both readers share; cond is a constant condition.
func (s *Store) recent(ctx context.Context, from time.Time, limit int, cond string, keys []string) ([]Request, error) {
	if limit <= 0 || limit > maxRecent {
		limit = maxRecent
	}
	clause, args, usable := keyClause(keys)
	if !usable {
		return []Request{}, nil
	}
	conds := make([]string, 0, 3)
	if !from.IsZero() {
		conds = append(conds, `"ts" >= ?`)
		args = append([]any{from.UnixMilli()}, args...)
	}
	if cond != "" {
		conds = append(conds, cond)
	}
	if clause != "" {
		conds = append(conds, strings.TrimPrefix(clause, " AND "))
	}
	where := ""
	if len(conds) > 0 {
		where = ` WHERE ` + strings.Join(conds, " AND ")
	}
	var rows []struct {
		TS           int64  `db:"ts"`
		Provider     string `db:"provider"`
		Model        string `db:"model"`
		ConnectionID string `db:"connectionId"`
		APIKeyName   string `db:"apiKeyName"`
		Prompt       int64  `db:"promptTokens"`
		Completion   int64  `db:"completionTokens"`
		Cached       int64  `db:"cachedTokens"`
		Status       int64  `db:"status"`
		ErrorClass   string `db:"errorClass"`
		Savers       string `db:"savers"`
		Skills       string `db:"skills"`
	}
	err := s.db.WithContext(ctx).Select(&rows, s.q(`SELECT "ts","provider","model","connectionId","apiKeyName","promptTokens",`+
		`"completionTokens","cachedTokens","status","errorClass","savers","skills" FROM "requests"`+where+` ORDER BY "ts" DESC, "id" DESC LIMIT ?`),
		append(args, limit)...)
	if err != nil {
		return nil, err
	}
	out := make([]Request, 0, len(rows))
	for _, r := range rows {
		out = append(out, Request{
			Time: time.UnixMilli(r.TS).UTC(), Provider: r.Provider, Model: r.Model, ConnectionID: r.ConnectionID, APIKeyName: r.APIKeyName,
			Prompt: r.Prompt, Completion: r.Completion, Cached: r.Cached, Status: r.Status, ErrorClass: r.ErrorClass,
			Savers: r.Savers, Skills: r.Skills,
		})
	}
	return out, nil
}

// Providers lists every provider the usage store has served, including days
// whose request rows are pruned, so a filter keeps working.
func (s *Store) Providers(ctx context.Context, keys []string) ([]string, error) {
	clause, args, usable := keyClause(keys)
	if !usable {
		return []string{}, nil
	}
	var out []string
	err := s.db.WithContext(ctx).Select(&out, s.q(`SELECT DISTINCT "provider" FROM "rollupDaily" WHERE "provider" <> ''`+clause+` ORDER BY "provider"`), args...)
	return out, err
}
