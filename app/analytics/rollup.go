package analytics

import (
	"context"
	"sort"
	"strings"
	"time"

	contractsdb "github.com/goravel/framework/contracts/database/db"
)

type rollupKey struct {
	bucket                                            int64
	provider, model, connectionID, apiKeyID, endpoint string
}

type rollupSum struct {
	requests, errors, prompt, completion, cached, cacheWrite, reasoning int64
	cost                                                                float64
}

func hourBucket(t time.Time) int64 { return t.UTC().Truncate(time.Hour).UnixMilli() }

func dayBucket(t time.Time) int64 {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC).UnixMilli()
}

func aggregate(rows []row, bucket func(time.Time) int64) map[rollupKey]*rollupSum {
	out := map[rollupKey]*rollupSum{}
	for _, r := range rows {
		k := rollupKey{bucket(r.Time), r.Provider, r.Model, r.ConnectionID, r.APIKeyID, r.Endpoint}
		v := out[k]
		if v == nil {
			v = &rollupSum{}
			out[k] = v
		}
		v.requests++
		if r.Status >= 400 {
			v.errors++
		}
		v.prompt += r.PromptTokens
		v.completion += r.CompletionTokens
		v.cached += r.CachedTokens
		v.cacheWrite += r.CacheWriteTokens
		v.reasoning += r.ReasoningTokens
		v.cost += r.Cost
	}
	return out
}

// sortedRollupKeys orders the dimensions the way PostgreSQL compares them, so
// two writers touch lock rows in the same order and cannot deadlock.
func sortedRollupKeys(m map[rollupKey]*rollupSum) []rollupKey {
	keys := make([]rollupKey, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.bucket != b.bucket {
			return a.bucket < b.bucket
		}
		for _, p := range [][2]string{{a.provider, b.provider}, {a.model, b.model}, {a.connectionID, b.connectionID}, {a.apiKeyID, b.apiKeyID}, {a.endpoint, b.endpoint}} {
			if p[0] != p[1] {
				return p[0] < p[1]
			}
		}
		return false
	})
	return keys
}

var sumCols = []string{"requests", "errors", "promptTokens", "completionTokens", "cachedTokens", "cacheWriteTokens", "reasoningTokens", "cost"}

func rollupUpsertSQL(table string) string {
	cols := []string{`"bucket"`, `"provider"`, `"model"`, `"connectionId"`, `"apiKeyId"`, `"endpoint"`}
	ph := make([]string, len(cols))
	for i := range ph {
		ph[i] = "?"
	}
	set := make([]string, 0, len(sumCols))
	for _, c := range sumCols {
		cols = append(cols, `"`+c+`"`)
		ph = append(ph, "?")
		set = append(set, `"`+c+`" = "`+table+`"."`+c+`" + excluded."`+c+`"`)
	}
	return `INSERT INTO "` + table + `"(` + strings.Join(cols, ",") + `) VALUES(` + strings.Join(ph, ",") +
		`) ON CONFLICT("bucket","provider","model","connectionId","apiKeyId","endpoint") DO UPDATE SET ` + strings.Join(set, ", ")
}

func (s *Store) addRollups(tx contractsdb.Tx, rows []row) error {
	for _, t := range []struct {
		table  string
		bucket func(time.Time) int64
	}{{"rollupHourly", hourBucket}, {"rollupDaily", dayBucket}} {
		sums := aggregate(rows, t.bucket)
		sql := s.q(rollupUpsertSQL(t.table))
		for _, k := range sortedRollupKeys(sums) {
			v := sums[k]
			err := tx.Statement(sql, k.bucket, k.provider, k.model, k.connectionID, k.apiKeyID, k.endpoint,
				v.requests, v.errors, v.prompt, v.completion, v.cached, v.cacheWrite, v.reasoning, v.cost)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// bumpLifetime adds n to the all-time request counter in the same transaction
// as the rows and their rollups.
func (s *Store) bumpLifetime(tx contractsdb.Tx, n int64) error {
	return tx.Statement(s.q(`INSERT INTO "meta"("key","value") VALUES(?,?) ON CONFLICT("key") DO UPDATE SET "value" = "meta"."value" + excluded."value"`), lifetimeKey, n)
}

func (s *Store) lifetime(ctx context.Context) (int64, error) {
	var one []int64
	err := s.db.WithContext(ctx).Select(&one, s.q(`SELECT "value" FROM "meta" WHERE "key" = ?`), lifetimeKey)
	if err != nil {
		return 0, err
	}
	if len(one) == 0 {
		return 0, nil
	}
	return one[0], nil
}
