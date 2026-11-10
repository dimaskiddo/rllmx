package analytics

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"rllmx/app/store/dialect"
	"strconv"
	"strings"
	"time"

	contractsdb "github.com/goravel/framework/contracts/database/db"
	"github.com/tidwall/gjson"

	"rllmx/app/store/repository"
	"rllmx/app/store/sealer"
)

const (
	defaultImportBatch = 5000
	seededKey          = "lifetimeSeeded"
)

// ImportOptions tunes ImportUsage; zero values give the defaults.
type ImportOptions struct {
	BatchSize int
	// WorkDir holds the temporary copy of the source. It defaults to a folder
	// next to the usage database file.
	WorkDir string
	// OpenKey turns a stored client key into the plain key old usage rows carry.
	// It is required when the source holds sealed keys.
	OpenKey func(stored string) (string, error)

	afterBatch func()
}

// ImportTotals sums what an import moved.
type ImportTotals struct {
	Rows             int64
	PromptTokens     int64
	CompletionTokens int64
	CachedTokens     int64
	CacheWriteTokens int64
	ReasoningTokens  int64
	Cost             float64
}

// ImportReport describes one run. Source is summed from the old table,
// Imported from the new one, so Verify compares two independent counts.
type ImportReport struct {
	Rows     int64
	Inserted int64
	Skipped  int64
	Invalid  int64
	Source   ImportTotals
	Imported ImportTotals

	fingerprint string
}

// Verify reports every counter on which the source and the imported rows disagree.
func (r ImportReport) Verify() error {
	var bad []string
	cmp := func(name string, src, got int64) {
		if src != got {
			bad = append(bad, fmt.Sprintf("%s: source %d, imported %d", name, src, got))
		}
	}
	cmp("rows", r.Source.Rows, r.Imported.Rows)
	cmp("prompt tokens", r.Source.PromptTokens, r.Imported.PromptTokens)
	cmp("completion tokens", r.Source.CompletionTokens, r.Imported.CompletionTokens)
	cmp("cached tokens", r.Source.CachedTokens, r.Imported.CachedTokens)
	cmp("cache write tokens", r.Source.CacheWriteTokens, r.Imported.CacheWriteTokens)
	cmp("reasoning tokens", r.Source.ReasoningTokens, r.Imported.ReasoningTokens)
	if diff := math.Abs(r.Source.Cost - r.Imported.Cost); diff > math.Max(1e-6, 1e-9*math.Abs(r.Source.Cost)) {
		bad = append(bad, fmt.Sprintf("cost: source %.9f, imported %.9f", r.Source.Cost, r.Imported.Cost))
	}
	if len(bad) > 0 {
		return fmt.Errorf("import verification failed: %s", strings.Join(bad, "; "))
	}
	return nil
}

// ImportUsage copies the usage history of an old database into the store,
// reading a private copy so the source stays untouched. The plain API key never
// reaches the new rows; a rerun adds nothing and an interrupted run resumes.
func (s *Store) ImportUsage(ctx context.Context, srcPath string, opt ImportOptions) (ImportReport, error) {
	var rep ImportReport
	if s.open == nil {
		return rep, errors.New("analytics: importing needs an opener for the source database")
	}
	if opt.BatchSize <= 0 {
		opt.BatchSize = defaultImportBatch
	}
	work := opt.WorkDir
	if work == "" {
		if s.file == "" {
			return rep, errors.New("analytics: a work directory is required for this database")
		}
		work = filepath.Join(filepath.Dir(s.file), "import-tmp")
	}
	defer os.Remove(work)
	snap, err := repository.OpenSnapshot(s.open, srcPath, work)
	if err != nil {
		return rep, fmt.Errorf("analytics: %w", err)
	}
	defer snap.Close()
	src := snap.DB()

	keys, err := readLegacyKeys(ctx, src, opt.OpenKey)
	if err != nil {
		return rep, err
	}
	if rep.Source, err = sourceTotals(ctx, src, opt.BatchSize); err != nil {
		return rep, fmt.Errorf("analytics: source has no readable usageHistory: %w", err)
	}
	if rep.fingerprint, err = sourceFingerprint(ctx, src); err != nil {
		return rep, err
	}

	var minTS, maxTS, lastID int64
	for {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		batch, next, invalid, err := readLegacyBatch(ctx, src, lastID, opt.BatchSize, keys, rep.fingerprint)
		if err != nil {
			return rep, err
		}
		if next == lastID {
			break
		}
		lastID = next
		rep.Invalid += invalid
		rep.Rows += int64(len(batch)) + invalid
		for _, r := range batch {
			ms := r.Time.UnixMilli()
			if minTS == 0 || ms < minTS {
				minTS = ms
			}
			if ms > maxTS {
				maxTS = ms
			}
		}
		n, err := s.importBatch(ctx, batch)
		if err != nil {
			return rep, err
		}
		rep.Inserted += n
		rep.Skipped += int64(len(batch)) - n
		if opt.afterBatch != nil {
			opt.afterBatch()
		}
	}
	if err := s.finishImport(ctx, src, minTS, maxTS, rep.Source.Rows); err != nil {
		return rep, err
	}
	rep.Imported, err = s.importedTotals(ctx, rep.fingerprint)
	return rep, err
}

// sourceFingerprint names a source by its first row, which the old program never
// deletes. Row ids repeat across sources, so an import id is the fingerprint plus
// the row id.
func sourceFingerprint(ctx context.Context, src contractsdb.DB) (string, error) {
	var rows []legacyRow
	err := src.WithContext(ctx).Select(&rows, `SELECT "id","timestamp","provider","model","connectionId","endpoint","status","tokens" FROM "usageHistory" ORDER BY "id" LIMIT 1`)
	if err != nil {
		return "", fmt.Errorf("analytics: fingerprint source: %w", err)
	}
	if len(rows) == 0 {
		return "", nil
	}
	r := rows[0]
	h := sha256.New()
	fmt.Fprintf(h, "%d", r.ID)
	for _, v := range []*string{r.Timestamp, r.Provider, r.Model, r.ConnectionID, r.Endpoint, r.Status, r.Tokens} {
		h.Write([]byte{0})
		if v != nil {
			h.Write([]byte(*v))
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}

type legacyKey struct{ id, name string }

// ErrSealedKeys means the source holds sealed client keys but no way to open them was given.
var ErrSealedKeys = errors.New("analytics: the source holds sealed client keys; set DB_ENCRYPTION_SECRET to its secret")

func readLegacyKeys(ctx context.Context, src contractsdb.DB, open func(string) (string, error)) (map[string]legacyKey, error) {
	out := map[string]legacyKey{}
	ok, err := repository.TableExists(ctx, src, "apiKeys")
	if err != nil || !ok {
		return out, err
	}
	var rows []struct {
		Key  string  `db:"key"`
		ID   string  `db:"id"`
		Name *string `db:"name"`
	}
	if err := src.Select(&rows, `SELECT "key","id","name" FROM "apiKeys"`); err != nil {
		return nil, err
	}
	for _, r := range rows {
		key := r.Key
		if sealer.IsSealed(key) {
			if open == nil {
				return nil, ErrSealedKeys
			}
			plain, err := open(key)
			if err != nil {
				return nil, fmt.Errorf("analytics: open a client key of the source: %w", err)
			}
			key = plain
		}
		name := ""
		if r.Name != nil {
			name = *r.Name
		}
		out[key] = legacyKey{r.ID, name}
	}
	return out, nil
}

// legacyRow is one old usage row; every optional column is a pointer because the
// old program left NULLs behind.
type legacyRow struct {
	ID               int64    `db:"id"`
	Timestamp        *string  `db:"timestamp"`
	Provider         *string  `db:"provider"`
	Model            *string  `db:"model"`
	ConnectionID     *string  `db:"connectionId"`
	APIKey           *string  `db:"apiKey"`
	Endpoint         *string  `db:"endpoint"`
	PromptTokens     *int64   `db:"promptTokens"`
	CompletionTokens *int64   `db:"completionTokens"`
	Cost             *float64 `db:"cost"`
	Status           *string  `db:"status"`
	Tokens           *string  `db:"tokens"`
}

func str(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func i64(n *int64) int64 {
	if n == nil {
		return 0
	}
	return *n
}

func f64(f *float64) float64 {
	if f == nil {
		return 0
	}
	return *f
}

// cachedTokens prefers the newer field the old program also wrote.
func cachedTokens(tokens string) int64 {
	if v, ok := tokenInt(tokens, "cached_tokens"); ok {
		return v
	}
	v, _ := tokenInt(tokens, "cache_read_input_tokens")
	return v
}

// sourceTotals walks the old table in id order. The validity rule lives in
// parseLegacyTime, which the row reader uses too, so the two passes cannot
// disagree about which rows count; the SQLite build reaches no scalar function
// that would express that rule in SQL.
func sourceTotals(ctx context.Context, src contractsdb.DB, batch int) (ImportTotals, error) {
	var t ImportTotals
	var lastID int64
	for {
		if err := ctx.Err(); err != nil {
			return t, err
		}
		var rows []struct {
			ID               int64    `db:"id"`
			Timestamp        *string  `db:"timestamp"`
			PromptTokens     *int64   `db:"promptTokens"`
			CompletionTokens *int64   `db:"completionTokens"`
			Cost             *float64 `db:"cost"`
			Tokens           *string  `db:"tokens"`
		}
		err := src.Select(&rows, `SELECT "id","timestamp","promptTokens","completionTokens","cost","tokens" FROM "usageHistory" WHERE "id" > ? ORDER BY "id" LIMIT ?`, lastID, batch)
		if err != nil {
			return t, err
		}
		if len(rows) == 0 {
			return t, nil
		}
		lastID = rows[len(rows)-1].ID
		for _, r := range rows {
			if _, ok := parseLegacyTime(str(r.Timestamp)); !ok {
				continue
			}
			tokens := str(r.Tokens)
			t.Rows++
			t.PromptTokens += i64(r.PromptTokens)
			t.CompletionTokens += i64(r.CompletionTokens)
			t.Cost += f64(r.Cost)
			if gjson.Valid(tokens) {
				t.CachedTokens += cachedTokens(tokens)
				t.CacheWriteTokens += mustInt(tokens, "cache_creation_input_tokens")
				t.ReasoningTokens += mustInt(tokens, "reasoning_tokens")
			}
		}
		if len(rows) < batch {
			return t, nil
		}
	}
}

func parseLegacyTime(s string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

func classForStatus(n int) string {
	switch {
	case n < 400:
		return "none"
	case n == 401 || n == 403:
		return "auth"
	case n == 404:
		return "not_found"
	case n == 408 || n == 504:
		return "timeout"
	case n == 429:
		return "rate_limit"
	case n >= 500:
		return "upstream_5xx"
	default:
		return "invalid_request"
	}
}

func legacyStatus(s *string) (int, string) {
	v := strings.TrimSpace(str(s))
	if v == "" || strings.EqualFold(v, "ok") || strings.EqualFold(v, "success") {
		return 200, "none"
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 100 || n > 599 {
		return 0, "unknown"
	}
	return n, classForStatus(n)
}

func tokenInt(tokens, field string) (int64, bool) {
	r := gjson.Get(tokens, field)
	if !r.Exists() {
		return 0, false
	}
	return r.Int(), true
}

func mustInt(tokens, field string) int64 {
	v, _ := tokenInt(tokens, field)
	return v
}

// readLegacyBatch returns the last id read (lastID when none), the rows that map
// cleanly and the count skipped for an unreadable timestamp.
func readLegacyBatch(ctx context.Context, src contractsdb.DB, lastID int64, limit int, keys map[string]legacyKey, fingerprint string) ([]row, int64, int64, error) {
	var rows []legacyRow
	err := src.Select(&rows, `SELECT "id","timestamp","provider","model","connectionId","apiKey","endpoint","promptTokens","completionTokens","cost","status","tokens" FROM "usageHistory" WHERE "id" > ? ORDER BY "id" LIMIT ?`, lastID, limit)
	if err != nil {
		return nil, lastID, 0, err
	}
	out := make([]row, 0, len(rows))
	var invalid int64
	next := lastID
	for _, r := range rows {
		if err := ctx.Err(); err != nil {
			return nil, lastID, 0, err
		}
		next = r.ID
		when, ok := parseLegacyTime(str(r.Timestamp))
		if !ok {
			invalid++
			continue
		}
		status, class := legacyStatus(r.Status)
		rec := Record{
			Time: when, Provider: str(r.Provider), Model: str(r.Model), ConnectionID: str(r.ConnectionID),
			Endpoint: str(r.Endpoint), Status: status, ErrorClass: class,
			PromptTokens: i64(r.PromptTokens), CompletionTokens: i64(r.CompletionTokens), Cost: f64(r.Cost),
		}
		if r.APIKey != nil {
			if k, found := keys[*r.APIKey]; found {
				rec.APIKeyID, rec.APIKeyName = k.id, k.name
			}
		}
		if tokens := str(r.Tokens); gjson.Valid(tokens) {
			rec.CachedTokens = cachedTokens(tokens)
			rec.CacheWriteTokens = mustInt(tokens, "cache_creation_input_tokens")
			rec.ReasoningTokens = mustInt(tokens, "reasoning_tokens")
		}
		imp := fingerprint + ":" + strconv.FormatInt(r.ID, 10)
		out = append(out, row{Record: sanitize(rec), importID: &imp})
	}
	return out, next, invalid, nil
}

const importSQL = insertSQL + ` ON CONFLICT("importId") DO NOTHING`

func (s *Store) importBatch(ctx context.Context, rows []row) (int64, error) {
	var inserted int64
	err := s.Tx(ctx, func(tx contractsdb.Tx) error {
		inserted = 0
		stmt := s.q(importSQL)
		for _, r := range rows {
			res, err := tx.Insert(stmt, rowArgs(r)...)
			if err != nil {
				return err
			}
			if res != nil {
				inserted += res.RowsAffected
			}
		}
		return nil
	})
	return inserted, err
}

func (s *Store) rebuildRollupSQL(table string, size int64) (string, string) {
	del := `DELETE FROM "` + table + `" WHERE "bucket" >= ? AND "bucket" < ?`
	bucket := `("ts" - ("ts" % ` + strconv.FormatInt(size, 10) + `))`
	dims := `"bucket","provider","model","connectionId","apiKeyId","endpoint"`
	group := bucket + `,"provider","model","connectionId","apiKeyId","endpoint"`
	ins := `INSERT INTO "` + table + `"(` + dims + `,"requests","errors","promptTokens","completionTokens","cachedTokens","cacheWriteTokens","reasoningTokens","cost") SELECT ` +
		bucket + `,"provider","model","connectionId","apiKeyId","endpoint",COUNT(*),SUM(CASE WHEN "status" >= 400 THEN 1 ELSE 0 END),SUM("promptTokens"),SUM("completionTokens"),SUM("cachedTokens"),SUM("cacheWriteTokens"),SUM("reasoningTokens"),SUM("cost") FROM "requests" WHERE "ts" >= ? AND "ts" < ? GROUP BY ` + group
	return del, ins
}

// finishImport rebuilds the rollups for the imported range and, once, adds the
// old lifetime counter; imported rows do not bump the live counter.
func (s *Store) finishImport(ctx context.Context, src contractsdb.DB, minTS, maxTS, validRows int64) error {
	var seed int64
	if ok, err := repository.TableExists(ctx, src, "_meta"); err != nil {
		return err
	} else if ok {
		var raw []string
		if err := src.Select(&raw, `SELECT "value" FROM "_meta" WHERE "key" = 'totalRequestsLifetime'`); err != nil {
			return fmt.Errorf("analytics: read the old lifetime counter: %w", err)
		}
		if len(raw) > 0 {
			seed, _ = strconv.ParseInt(strings.TrimSpace(raw[0]), 10, 64)
		}
	}
	if seed < validRows {
		seed = validRows
	}
	return s.Tx(ctx, func(tx contractsdb.Tx) error {
		if maxTS > 0 {
			for _, t := range []struct {
				table string
				size  int64
			}{{"rollupHourly", int64(time.Hour / time.Millisecond)}, {"rollupDaily", int64(24 * time.Hour / time.Millisecond)}} {
				lo := minTS - minTS%t.size
				hi := maxTS - maxTS%t.size + t.size
				del, ins := s.rebuildRollupSQL(t.table, t.size)
				if err := tx.Statement(s.q(del), lo, hi); err != nil {
					return err
				}
				if err := tx.Statement(s.q(ins), lo, hi); err != nil {
					return err
				}
			}
		}
		var done []int64
		if err := tx.Select(&done, s.q(`SELECT "value" FROM "meta" WHERE "key" = ?`), seededKey); err != nil {
			return err
		}
		if len(done) > 0 {
			return nil
		}
		if err := s.bumpLifetime(tx, seed); err != nil {
			return err
		}
		return tx.Statement(s.q(`INSERT INTO "meta"("key","value") VALUES(?,1)`), seededKey)
	})
}

func (s *Store) importedTotals(ctx context.Context, fingerprint string) (ImportTotals, error) {
	var rows []struct {
		Rows       int64   `db:"rows"`
		Prompt     int64   `db:"prompt"`
		Completion int64   `db:"completion"`
		Cached     int64   `db:"cached"`
		CacheWrite int64   `db:"cacheWrite"`
		Reasoning  int64   `db:"reasoning"`
		Cost       float64 `db:"cost"`
	}
	err := s.db.WithContext(ctx).Select(&rows, s.q(`SELECT COUNT(*) AS "rows", COALESCE(SUM("promptTokens"),0) AS "prompt", COALESCE(SUM("completionTokens"),0) AS "completion", COALESCE(SUM("cachedTokens"),0) AS "cached", COALESCE(SUM("cacheWriteTokens"),0) AS "cacheWrite", COALESCE(SUM("reasoningTokens"),0) AS "reasoning", COALESCE(SUM("cost"),0) AS "cost" FROM "requests" WHERE "importId" LIKE ?`), fingerprint+":%")
	if err != nil {
		return ImportTotals{}, err
	}
	if len(rows) == 0 {
		return ImportTotals{}, nil
	}
	r := rows[0]
	return ImportTotals{
		Rows: r.Rows, PromptTokens: r.Prompt, CompletionTokens: r.Completion, CachedTokens: r.Cached,
		CacheWriteTokens: r.CacheWrite, ReasoningTokens: r.Reasoning, Cost: r.Cost,
	}, nil
}

// DropLegacyTables removes the three old usage tables from the database at path.
// confirm must be exactly "DROP": the old program can still open the file, but
// its usage history is gone.
func (s *Store) DropLegacyTables(ctx context.Context, path, confirm string) error {
	if confirm != "DROP" {
		return errors.New("analytics: dropping the old usage tables needs the confirmation word DROP")
	}
	if s.open == nil {
		return errors.New("analytics: dropping the old usage tables needs an opener for the source database")
	}
	if _, err := os.Stat(path); err != nil {
		return err
	}
	c, err := s.open(path, dialect.BusyWait)
	if err != nil {
		return err
	}
	defer c.Close()
	return c.DB.WithContext(ctx).Transaction(func(tx contractsdb.Tx) error {
		for _, t := range []string{"usageHistory", "usageDaily", "requestDetails"} {
			if err := tx.Statement(`DROP TABLE IF EXISTS "` + t + `"`); err != nil {
				return err
			}
		}
		return nil
	})
}
