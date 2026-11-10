package analytics

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"rllmx/app/store/dialect"
	"testing"
	"time"

	"rllmx/app/store/repository"
	"rllmx/tests"
	"rllmx/tests/store"
)

// writableQuery is the connection string a test file is opened with. The
// auto_vacuum pragma comes first because it only takes effect on a file with no
// tables yet, which is exactly how the sqlite_analytics connection sets it.
const writableQuery = "_txlock=immediate&_pragma=busy_timeout(5000)&_pragma=auto_vacuum(2)" +
	"&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=secure_delete(ON)"

// testStore keeps the connection under the store, so a test can open the same
// file again the way the provider would.
type testStore struct {
	*Store
	sq   *tests.Conn
	path string
}

// newStore is a store on the engine the suite runs on. On SQLite it is a file in
// the test's own folder, laid out the way the real one is:
// DATA_DIR/db/analytics.sqlite.
func newStore(t testing.TB) *testStore {
	t.Helper()
	if tests.UsingPostgres() {
		sq := tests.OpenPostgres(t)
		s, err := Open(context.Background(), Options{
			DB: sq.DB, Schema: sq.Schema, Log: sq.Log, Dialect: dialect.Postgres,
			PGSchema: sq.PGSchema, Version: "test", Open: testOpener(t),
		})
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		t.Cleanup(func() { _ = s.Close(context.Background()) })
		return &testStore{Store: s, sq: sq}
	}
	return openAt(t, filepath.Join(t.TempDir(), "db", "analytics.sqlite"))
}

// openAt is the SQLite path: a named file, prepared before the driver opens it.
func openAt(t testing.TB, path string) *testStore {
	t.Helper()
	if err := Prepare(path); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	sq := tests.OpenSQLiteWithQuery(t, path, writableQuery)
	s, err := Open(context.Background(), Options{
		DB: sq.DB, Schema: sq.Schema, Log: sq.Log, Dialect: dialect.SQLite,
		File: path, Version: "test", Open: testOpener(t),
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	return &testStore{Store: s, sq: sq, path: path}
}

func testOpener(t testing.TB) repository.Opener {
	t.Helper()
	return store.Opener(t)
}

func count(t testing.TB, s *testStore, table string) int64 {
	t.Helper()
	var n []int64
	if err := s.db.WithContext(context.Background()).Select(&n, `SELECT count(*) FROM "`+table+`"`); err != nil {
		t.Fatal(err)
	}
	if len(n) == 0 {
		return 0
	}
	return n[0]
}

var t0 = time.Date(2026, 3, 14, 10, 30, 15, 0, time.UTC)

func rec(i int, ts time.Time) Record {
	return Record{
		Time: ts, RequestID: fmt.Sprintf("r%d", i), APIKeyID: fmt.Sprintf("k%d", i%3), APIKeyName: "name",
		ConnectionID: fmt.Sprintf("c%d", i%2), Provider: []string{"openai", "claude"}[i%2], Model: fmt.Sprintf("m%d", i%4),
		Endpoint: "/v1/chat/completions", Status: 200, PromptTokens: int64(10 + i), CompletionTokens: int64(5 + i),
		CachedTokens: int64(i % 3), CacheWriteTokens: int64(i % 2), ReasoningTokens: int64(i % 5),
		Cost: 0.001 * float64(i+1), LatencyMs: int64(100 + i),
	}
}

func seed(t testing.TB, s *testStore, n int, start time.Time, step time.Duration) {
	t.Helper()
	w := s.NewWriter(WriterOptions{QueueSize: n + 10})
	for i := range n {
		w.Enqueue(rec(i, start.Add(time.Duration(i)*step)))
	}
	if err := w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// legacyFile is the source of an import: a database in the old program's shape,
// written through the same factory the production opener uses.
const legacyDDL = `CREATE TABLE "usageHistory" ("id" INTEGER PRIMARY KEY AUTOINCREMENT, "timestamp" TEXT NOT NULL, "provider" TEXT, "model" TEXT, "connectionId" TEXT, "apiKey" TEXT, "endpoint" TEXT, "promptTokens" INTEGER DEFAULT 0, "completionTokens" INTEGER DEFAULT 0, "cost" REAL DEFAULT 0, "status" TEXT, "tokens" TEXT, "meta" TEXT);
CREATE TABLE "apiKeys" ("id" TEXT PRIMARY KEY, "key" TEXT UNIQUE NOT NULL, "name" TEXT, "machineId" TEXT, "isActive" INTEGER DEFAULT 1, "createdAt" TEXT NOT NULL);
CREATE TABLE "_meta" ("key" TEXT PRIMARY KEY, "value" TEXT NOT NULL);
CREATE TABLE "usageDaily" ("dateKey" TEXT PRIMARY KEY, "data" TEXT NOT NULL);
CREATE TABLE "requestDetails" ("id" TEXT PRIMARY KEY, "data" TEXT);`

type oldRow struct {
	ts, provider, model, conn, key, endpoint, status, tokens string
	prompt, completion                                       int64
	cost                                                     float64
}

func buildLegacy(t testing.TB, rows []oldRow, keys map[string]string) string {
	t.Helper()
	return buildLegacyWith(t, rows, keys, "_txlock=immediate&_pragma=busy_timeout(5000)")
}

func buildLegacyWith(t testing.TB, rows []oldRow, keys map[string]string, query string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "legacy.sqlite")
	sq := tests.OpenSQLiteWithQuery(t, path, query)
	ctx := context.Background()
	for _, stmt := range tests.SplitStatements(legacyDDL) {
		if err := sq.DB.WithContext(ctx).Statement(stmt); err != nil {
			t.Fatalf("legacy ddl: %v", err)
		}
	}
	for secret, id := range keys {
		err := sq.DB.WithContext(ctx).Statement(
			`INSERT INTO "apiKeys"("id","key","name","machineId","createdAt") VALUES(?,?,?,?,?)`,
			id, secret, "name-of-"+id, "m", "2026-01-01T00:00:00.000Z")
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range rows {
		err := sq.DB.WithContext(ctx).Statement(
			`INSERT INTO "usageHistory"("timestamp","provider","model","connectionId","apiKey","endpoint","promptTokens","completionTokens","cost","status","tokens","meta") VALUES(?,?,?,?,?,?,?,?,?,?,?,'{}')`,
			r.ts, nullIfEmpty(r.provider), nullIfEmpty(r.model), nullIfEmpty(r.conn), nullIfEmpty(r.key),
			nullIfEmpty(r.endpoint), r.prompt, r.completion, r.cost, nullIfEmpty(r.status), nullIfEmpty(r.tokens))
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := sq.DB.WithContext(ctx).Statement(`INSERT INTO "_meta" VALUES('totalRequestsLifetime', ?)`, fmt.Sprint(len(rows)+500)); err != nil {
		t.Fatal(err)
	}
	return path
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func sampleRows(n int) []oldRow {
	base := time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC)
	out := make([]oldRow, n)
	for i := range out {
		out[i] = oldRow{
			ts:       base.Add(time.Duration(i) * 7 * time.Minute).Format("2006-01-02T15:04:05.000Z"),
			provider: []string{"openai", "claude", "gemini"}[i%3],
			model:    fmt.Sprintf("m%d", i%5), conn: fmt.Sprintf("c%d", i%4),
			key:      []string{"sk-aaaa-111111-aaaaaaaa", "sk-bbbb-222222-bbbbbbbb", ""}[i%3],
			endpoint: []string{"/v1/chat/completions", "/v1/v1/messages", ""}[i%3], status: "ok",
			tokens: fmt.Sprintf(`{"prompt_tokens":%d,"completion_tokens":%d,"cache_read_input_tokens":%d,"cache_creation_input_tokens":%d,"reasoning_tokens":%d,"cached_tokens":%d}`, 10+i, 5+i, i%4, i%3, i%5, i%2),
			prompt: int64(10 + i), completion: int64(5 + i), cost: 0.0001 * float64(i+1),
		}
	}
	return out
}

var legacyKeys = map[string]string{"sk-aaaa-111111-aaaaaaaa": "key-a", "sk-bbbb-222222-bbbbbbbb": "key-b"}

// sizeOf is the file size, read after a checkpoint so the number is the whole
// database rather than just the main file.
func sizeOf(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}
