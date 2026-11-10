package analytics

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rllmx/app/store/sealer"
	"rllmx/tests"
)

func TestImportMapsRowsWithoutCopyingTheKey(t *testing.T) {
	src := buildLegacy(t, sampleRows(60), legacyKeys)
	s := newStore(t)
	rep, err := s.ImportUsage(context.Background(), src, ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Rows != 60 || rep.Inserted != 60 || rep.Skipped != 0 {
		t.Fatalf("report = %+v", rep)
	}
	if got := count(t, s, "requests"); got != 60 {
		t.Fatalf("requests = %d", got)
	}
	var mapped []struct {
		WithID   int64 `db:"withId"`
		WithName int64 `db:"withName"`
		Empty    int64 `db:"empty"`
	}
	err = s.db.Select(&mapped, `SELECT count(CASE WHEN "apiKeyId" <> '' THEN 1 END) AS "withId", count(CASE WHEN "apiKeyName" LIKE 'name-of-%' THEN 1 END) AS "withName", count(CASE WHEN "apiKeyId" = '' THEN 1 END) AS "empty" FROM "requests"`)
	if err != nil {
		t.Fatal(err)
	}
	if mapped[0].WithID != 40 || mapped[0].WithName != 40 || mapped[0].Empty != 20 {
		t.Fatalf("key mapping: %+v", mapped[0])
	}
	for _, v := range storedStrings(t, s) {
		if strings.Contains(v, "sk-") {
			t.Fatalf("a raw key reached the usage store: %q", v)
		}
	}
}

// storedStrings is every text column of every stored row, for a test that looks
// for something that must never be there.
func storedStrings(t testing.TB, s *testStore) []string {
	t.Helper()
	var rows []struct {
		RequestID    string  `db:"requestId"`
		APIKeyID     string  `db:"apiKeyId"`
		APIKeyName   string  `db:"apiKeyName"`
		ConnectionID string  `db:"connectionId"`
		Provider     string  `db:"provider"`
		Model        string  `db:"model"`
		Endpoint     string  `db:"endpoint"`
		SourceFormat string  `db:"sourceFormat"`
		ErrorClass   string  `db:"errorClass"`
		ComboName    string  `db:"comboName"`
		ImportID     *string `db:"importId"`
	}
	err := s.db.Select(&rows, `SELECT "requestId","apiKeyId","apiKeyName","connectionId","provider","model","endpoint","sourceFormat","errorClass","comboName","importId" FROM "requests"`)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range rows {
		out = append(out, r.RequestID, r.APIKeyID, r.APIKeyName, r.ConnectionID, r.Provider, r.Model,
			r.Endpoint, r.SourceFormat, r.ErrorClass, r.ComboName)
		if r.ImportID != nil {
			out = append(out, *r.ImportID)
		}
	}
	return out
}

func TestImportKeepsEndpointAsStoredAndMapsStatus(t *testing.T) {
	rows := []oldRow{
		{ts: "2026-05-01T08:00:00.000Z", provider: "p", model: "m", endpoint: "/v1/v1/messages", status: "ok", tokens: `{}`},
		{ts: "2026-05-01T08:01:00.000Z", provider: "p", model: "m", status: "429", tokens: `{}`},
		{ts: "2026-05-01T08:02:00.000Z", provider: "p", model: "m", status: "weird", tokens: `{}`},
		{ts: "2026-05-01T08:03:00.000Z", provider: "p", model: "m", status: "", tokens: ``},
	}
	s := newStore(t)
	if _, err := s.ImportUsage(context.Background(), buildLegacy(t, rows, nil), ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	type got struct {
		Endpoint string `db:"endpoint"`
		Class    string `db:"errorClass"`
		Status   int64  `db:"status"`
	}
	var out []got
	if err := s.db.Select(&out, `SELECT "endpoint","errorClass","status" FROM "requests" ORDER BY "ts"`); err != nil {
		t.Fatal(err)
	}
	want := []got{{"/v1/v1/messages", "none", 200}, {"", "rate_limit", 429}, {"", "unknown", 0}, {"", "none", 200}}
	for i := range want {
		if out[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, out[i], want[i])
		}
	}
}

func TestImportTokenMappingAndCost(t *testing.T) {
	rows := []oldRow{{
		ts: "2026-05-01T08:00:00.000Z", provider: "p", model: "m", status: "ok", cost: 1.5e-7, prompt: 7, completion: 3,
		tokens: `{"prompt_tokens":7,"completion_tokens":3,"cache_read_input_tokens":4,"cache_creation_input_tokens":2,"reasoning_tokens":1}`,
	}, {
		ts: "2026-05-01T08:01:00.000Z", provider: "p", model: "m", status: "ok", prompt: 9, completion: 1,
		tokens: `{"input_tokens":9,"output_tokens":1,"cached_tokens":5}`,
	}}
	s := newStore(t)
	if _, err := s.ImportUsage(context.Background(), buildLegacy(t, rows, nil), ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	type tok struct {
		Prompt     int64   `db:"prompt"`
		Completion int64   `db:"completion"`
		Cached     int64   `db:"cached"`
		CacheWrite int64   `db:"cacheWrite"`
		Reasoning  int64   `db:"reasoning"`
		Cost       float64 `db:"cost"`
	}
	var out []tok
	err := s.db.Select(&out, `SELECT "promptTokens" AS "prompt","completionTokens" AS "completion","cachedTokens" AS "cached","cacheWriteTokens" AS "cacheWrite","reasoningTokens" AS "reasoning","cost" AS "cost" FROM "requests" ORDER BY "ts"`)
	if err != nil {
		t.Fatal(err)
	}
	want := []tok{{7, 3, 4, 2, 1, 1.5e-7}, {9, 1, 5, 0, 0, 0}}
	if len(out) != 2 {
		t.Fatalf("rows = %d", len(out))
	}
	for i := range want {
		if out[i].Prompt != want[i].Prompt || out[i].Completion != want[i].Completion || out[i].Cached != want[i].Cached ||
			out[i].CacheWrite != want[i].CacheWrite || out[i].Reasoning != want[i].Reasoning ||
			math.Abs(out[i].Cost-want[i].Cost) > 1e-15 {
			t.Errorf("row %d = %+v, want %+v", i, out[i], want[i])
		}
	}
}

func TestImportIsIdempotent(t *testing.T) {
	src := buildLegacy(t, sampleRows(80), legacyKeys)
	s := newStore(t)
	first, err := s.ImportUsage(context.Background(), src, ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.ImportUsage(context.Background(), src, ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if first.Inserted != 80 || second.Inserted != 0 || second.Skipped != 80 {
		t.Fatalf("first %+v second %+v", first, second)
	}
	if got := count(t, s, "requests"); got != 80 {
		t.Fatalf("requests = %d after two runs", got)
	}
	var reqs []int64
	if err := s.db.Select(&reqs, `SELECT sum("requests") FROM "rollupDaily"`); err != nil {
		t.Fatal(err)
	}
	if reqs[0] != 80 {
		t.Fatalf("rollups count %d requests after two runs", reqs[0])
	}
	if n, _ := s.lifetime(context.Background()); n != 580 {
		t.Fatalf("lifetime = %d, want 580 (seeded once from the old counter)", n)
	}
}

func TestImportResumesAfterAnInterruption(t *testing.T) {
	src := buildLegacy(t, sampleRows(100), legacyKeys)
	s := newStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	_, err := s.ImportUsage(ctx, src, ImportOptions{BatchSize: 10, afterBatch: func() {
		calls++
		if calls == 4 {
			cancel()
		}
	}})
	if err == nil {
		t.Fatal("a cancelled import reported success")
	}
	partial := count(t, s, "requests")
	if partial == 0 || partial >= 100 {
		t.Fatalf("partial = %d", partial)
	}
	rep, err := s.ImportUsage(context.Background(), src, ImportOptions{BatchSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got := count(t, s, "requests"); got != 100 || rep.Inserted != 100-partial {
		t.Fatalf("after resume: %d rows, inserted %d, partial %d", got, rep.Inserted, partial)
	}
	if err := rep.Verify(); err != nil {
		t.Fatalf("verification after resume: %v", err)
	}
	if n, _ := s.lifetime(context.Background()); n != 600 {
		t.Fatalf("lifetime = %d, want 600", n)
	}
}

func TestImportRebuildsRollupsExactly(t *testing.T) {
	src := buildLegacy(t, sampleRows(150), legacyKeys)
	s := newStore(t)
	w := s.NewWriter(WriterOptions{})
	w.Enqueue(Record{Time: time.Date(2026, 5, 1, 8, 5, 0, 0, time.UTC), Provider: "openai", Model: "live", Status: 200})
	if err := w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ImportUsage(context.Background(), src, ImportOptions{BatchSize: 25}); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"rollupHourly", "rollupDaily"} {
		var a []struct {
			Requests   int64   `db:"requests"`
			Prompt     int64   `db:"prompt"`
			CacheWrite int64   `db:"cacheWrite"`
			Cost       float64 `db:"cost"`
		}
		err := s.db.Select(&a, `SELECT sum("requests") AS "requests", sum("promptTokens") AS "prompt", sum("cacheWriteTokens") AS "cacheWrite", sum("cost") AS "cost" FROM "`+table+`"`)
		if err != nil {
			t.Fatal(err)
		}
		var b []struct {
			Requests   int64   `db:"requests"`
			Prompt     int64   `db:"prompt"`
			CacheWrite int64   `db:"cacheWrite"`
			Cost       float64 `db:"cost"`
		}
		err = s.db.Select(&b, `SELECT count(*) AS "requests", sum("promptTokens") AS "prompt", sum("cacheWriteTokens") AS "cacheWrite", sum("cost") AS "cost" FROM "requests"`)
		if err != nil {
			t.Fatal(err)
		}
		if a[0].Requests != b[0].Requests || a[0].Prompt != b[0].Prompt || a[0].CacheWrite != b[0].CacheWrite ||
			math.Abs(a[0].Cost-b[0].Cost) > 1e-9 {
			t.Errorf("%s: %+v vs %+v", table, a[0], b[0])
		}
	}
}

func TestImportVerifyChecksCountsTokensAndCost(t *testing.T) {
	src := buildLegacy(t, sampleRows(40), legacyKeys)
	s := newStore(t)
	rep, err := s.ImportUsage(context.Background(), src, ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := rep.Verify(); err != nil {
		t.Fatalf("clean import failed verification: %v", err)
	}
	bad := rep
	bad.Source.PromptTokens++
	if bad.Verify() == nil {
		t.Error("a prompt token mismatch passed")
	}
	bad = rep
	bad.Imported.CacheWriteTokens--
	if bad.Verify() == nil {
		t.Error("a cache write token mismatch passed")
	}
	bad = rep
	bad.Source.Cost += 1e-3
	if bad.Verify() == nil {
		t.Error("a cost mismatch passed")
	}
	bad = rep
	bad.Source.Rows++
	if bad.Verify() == nil {
		t.Error("a row count mismatch passed")
	}
}

func TestImportDoesNotModifyTheSource(t *testing.T) {
	src := buildLegacy(t, sampleRows(30), legacyKeys)
	before, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	s := newStore(t)
	if _, err := s.ImportUsage(context.Background(), src, ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("the source database changed")
	}
	if _, err := os.Stat(src + "-wal"); err == nil {
		t.Fatal("a WAL file appeared next to the source")
	}
}

func TestImportOfAMissingOrUnreadableSourceFails(t *testing.T) {
	s := newStore(t)
	if _, err := s.ImportUsage(context.Background(), filepath.Join(t.TempDir(), "none.sqlite"), ImportOptions{}); err == nil {
		t.Fatal("missing source accepted")
	}
	if got := count(t, s, "requests"); got != 0 {
		t.Fatal("rows appeared")
	}
	notDB := filepath.Join(t.TempDir(), "x.sqlite")
	if err := os.WriteFile(notDB, []byte("not a database at all, just text"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ImportUsage(context.Background(), notDB, ImportOptions{}); err == nil {
		t.Fatal("a non-database source accepted")
	}
}

func TestImportKeepsDuplicateAndOutOfOrderRows(t *testing.T) {
	r := oldRow{ts: "2026-05-01T08:00:00.000Z", provider: "p", model: "m", status: "ok", tokens: `{}`, prompt: 1}
	older := r
	older.ts = "2026-04-01T08:00:00.000Z"
	s := newStore(t)
	if _, err := s.ImportUsage(context.Background(), buildLegacy(t, []oldRow{r, r, older}, nil), ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := count(t, s, "requests"); got != 3 {
		t.Fatalf("%d rows kept of 3: duplicates are real data and must not be merged", got)
	}
}

func TestImportRejectsBadTimestampsWithoutLosingTheRest(t *testing.T) {
	rows := []oldRow{
		{ts: "2026-05-01T08:00:00.000Z", provider: "p", model: "m", status: "ok", tokens: `{}`},
		{ts: "not a time", provider: "p", model: "m", status: "ok", tokens: `{}`},
		{ts: "2026-05-01T08:02:00.000Z", provider: "p", model: "m", status: "ok", tokens: `{}`},
	}
	s := newStore(t)
	rep, err := s.ImportUsage(context.Background(), buildLegacy(t, rows, nil), ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Inserted != 2 || rep.Invalid != 1 {
		t.Fatalf("report = %+v", rep)
	}
}

func TestImportNeverLowersTheLifetimeCounter(t *testing.T) {
	src := buildLegacy(t, sampleRows(10), legacyKeys)
	s := newStore(t)
	w := s.NewWriter(WriterOptions{})
	for i := range 2000 {
		w.Enqueue(rec(i, t0))
	}
	if err := w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ImportUsage(context.Background(), src, ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.lifetime(context.Background()); n < 2000 {
		t.Fatalf("lifetime = %d, lowered by the import", n)
	}
}

func TestDropLegacyTablesNeedsTheTypedWord(t *testing.T) {
	src := buildLegacy(t, sampleRows(5), legacyKeys)
	s := newStore(t)
	if err := s.DropLegacyTables(context.Background(), src, "drop"); err == nil {
		t.Fatal("wrong confirmation accepted")
	}
	if err := s.DropLegacyTables(context.Background(), src, ""); err == nil {
		t.Fatal("empty confirmation accepted")
	}
	if got := legacyCount(t, src, "usageHistory"); got != 5 {
		t.Fatal("a refused drop changed the source")
	}
	if err := s.DropLegacyTables(context.Background(), src, "DROP"); err != nil {
		t.Fatal(err)
	}
	for _, tb := range []string{"usageHistory", "usageDaily", "requestDetails"} {
		if got := legacyCount(t, src, tb); got != 0 {
			t.Errorf("%s still exists", tb)
		}
	}
	for _, tb := range []string{"apiKeys", "_meta"} {
		if got := legacyCount(t, src, tb); got == 0 {
			t.Errorf("%s was dropped too", tb)
		}
	}
}

// legacyCount reads the source file directly: it is the old program's shape, not
// the store's.
func legacyCount(t testing.TB, path, table string) int64 {
	t.Helper()
	sq := tests.OpenSQLiteWithQuery(t, path, "mode=ro&_pragma=busy_timeout(5000)")
	var n []int64
	if err := sq.DB.Select(&n, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table); err != nil {
		t.Fatal(err)
	}
	if n[0] == 0 {
		return 0
	}
	var rows []int64
	if err := sq.DB.Select(&rows, `SELECT count(*) FROM "`+table+`"`); err != nil {
		t.Fatal(err)
	}
	return rows[0]
}

func TestImportBlanksTheKeyOfARowWhoseKeyIsNotInTheKeyTable(t *testing.T) {
	rows := []oldRow{
		{ts: "2026-05-01T08:00:00.000Z", provider: "p", model: "m", key: "sk-deleted-999999-cccccccc", status: "ok", tokens: `{}`},
		{ts: "2026-05-01T08:01:00.000Z", provider: "p", model: "m", key: "sk-aaaa-111111-aaaaaaaa", status: "ok", tokens: `{}`},
		{ts: "2026-05-01T08:02:00.000Z", provider: "p", model: "m", status: "ok", tokens: `{}`},
	}
	s := newStore(t)
	if _, err := s.ImportUsage(context.Background(), buildLegacy(t, rows, legacyKeys), ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	at := func(min int) []string {
		var v []string
		err := s.db.Select(&v, `SELECT "apiKeyId" FROM "requests" WHERE "ts" = ?`, time.Date(2026, 5, 1, 8, min, 0, 0, time.UTC).UnixMilli())
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	var names []string
	if err := s.db.Select(&names, `SELECT "apiKeyName" FROM "requests" WHERE "ts" = ?`, time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if at(0)[0] != "" || names[0] != "" || at(1)[0] != "key-a" || at(2)[0] != "" {
		t.Fatalf("orphan=%q/%q known=%q null=%q", at(0)[0], names[0], at(1)[0], at(2)[0])
	}
}

func TestImportSeedsTheCounterWithAtLeastTheRowCount(t *testing.T) {
	src := buildLegacy(t, sampleRows(30), legacyKeys)
	if err := writeLegacy(t, src, `UPDATE "_meta" SET "value" = '3' WHERE "key" = 'totalRequestsLifetime'`); err != nil {
		t.Fatal(err)
	}
	s := newStore(t)
	if _, err := s.ImportUsage(context.Background(), src, ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.lifetime(context.Background()); n != 30 {
		t.Fatalf("lifetime = %d, want 30: a stale counter must not be lower than the rows imported", n)
	}
}

func writeLegacy(t testing.TB, path, stmt string) error {
	t.Helper()
	sq := tests.OpenSQLiteWithQuery(t, path, "_txlock=immediate&_pragma=busy_timeout(5000)")
	return sq.DB.Statement(stmt)
}

func TestImportSeedsOnceEvenWhenTheCounterIsMissing(t *testing.T) {
	src := buildLegacy(t, sampleRows(12), legacyKeys)
	if err := writeLegacy(t, src, `DELETE FROM "_meta"`); err != nil {
		t.Fatal(err)
	}
	s := newStore(t)
	for range 3 {
		if _, err := s.ImportUsage(context.Background(), src, ImportOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if n, _ := s.lifetime(context.Background()); n != 12 {
		t.Fatalf("lifetime = %d after three runs, want 12", n)
	}
}

func TestImportLeavesNoTemporaryCopyBehind(t *testing.T) {
	tests.RequireSQLite(t)
	s := newStore(t)
	if _, err := s.ImportUsage(context.Background(), buildLegacy(t, sampleRows(5), legacyKeys), ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(filepath.Dir(s.path), "import-tmp")
	entries, _ := os.ReadDir(work)
	if len(entries) != 0 {
		t.Fatalf("%d temporary copies left", len(entries))
	}
	if _, err := s.ImportUsage(context.Background(), filepath.Join(t.TempDir(), "gone.sqlite"), ImportOptions{}); err == nil {
		t.Fatal("expected an error")
	}
	entries, _ = os.ReadDir(work)
	if len(entries) != 0 {
		t.Fatalf("%d temporary copies left after a failed run", len(entries))
	}
}

func TestImportReadsRowsThatAreStillInTheSourceWAL(t *testing.T) {
	path := buildLegacyWith(t, sampleRows(25), nil, "_txlock=immediate&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=wal_autocheckpoint(0)")
	if info, err := os.Stat(path + "-wal"); err != nil || info.Size() == 0 {
		t.Skipf("rows did not stay in the WAL (%v)", err)
	}
	s := newStore(t)
	rep, err := s.ImportUsage(context.Background(), path, ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Inserted != 25 {
		t.Fatalf("imported %d of 25 rows that were still in the WAL", rep.Inserted)
	}
}

func TestImportWorkDirIsOptionalForTheCallerAndRemovedWhenEmpty(t *testing.T) {
	tests.RequireSQLite(t)
	s := newStore(t)
	work := filepath.Join(t.TempDir(), "custom-work")
	if _, err := s.ImportUsage(context.Background(), buildLegacy(t, sampleRows(5), legacyKeys), ImportOptions{WorkDir: work}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(work); err == nil {
		t.Fatal("the work directory was left behind")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(s.path), "import-tmp")); err == nil {
		t.Fatal("the default work directory was used although another was given")
	}
}

func TestTwoSourcesWithTheSameRowIdsBothImportInFull(t *testing.T) {
	a := buildLegacy(t, sampleRows(30), legacyKeys)
	other := sampleRows(20)
	for i := range other {
		other[i].model = "other-" + other[i].model
	}
	b := buildLegacy(t, other, legacyKeys)
	s := newStore(t)
	ra, err := s.ImportUsage(context.Background(), a, ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rb, err := s.ImportUsage(context.Background(), b, ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if ra.Inserted != 30 || rb.Inserted != 20 || rb.Skipped != 0 {
		t.Fatalf("a %+v b %+v", ra, rb)
	}
	if err := ra.Verify(); err != nil {
		t.Fatal(err)
	}
	if err := rb.Verify(); err != nil {
		t.Fatalf("totals must be taken per source: %v", err)
	}
	if got := count(t, s, "requests"); got != 50 {
		t.Fatalf("requests = %d, want 50", got)
	}
}

func TestSourceTotalsUseTheSameTimestampRuleAsTheRowReader(t *testing.T) {
	rows := []oldRow{
		{ts: "2026-05-01T08:00:00.000Z", provider: "p", model: "m", status: "ok", tokens: `{}`, prompt: 5},
		{ts: "2026-05-01 08:01", provider: "p", model: "m", status: "ok", tokens: `{}`, prompt: 7},
		{ts: "2026-05-01", provider: "p", model: "m", status: "ok", tokens: `{}`, prompt: 11},
	}
	s := newStore(t)
	rep, err := s.ImportUsage(context.Background(), buildLegacy(t, rows, nil), ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Inserted != 1 || rep.Invalid != 2 || rep.Source.Rows != 1 || rep.Source.PromptTokens != 5 {
		t.Fatalf("%+v", rep)
	}
	if err := rep.Verify(); err != nil {
		t.Fatal(err)
	}
}

func TestReadLegacyKeysTellsAMissingTableFromAFailure(t *testing.T) {
	sq := tests.NewSQLite(t)
	keys, err := readLegacyKeys(context.Background(), sq.DB, nil)
	if err != nil || len(keys) != 0 {
		t.Fatalf("a source without apiKeys: %v %v", keys, err)
	}
	if err := sq.DB.Statement(`CREATE TABLE "apiKeys" ("key" TEXT, "id" TEXT, "name" TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err := sq.DB.Statement(`INSERT INTO "apiKeys" VALUES (NULL, 'id1', 'n')`); err != nil {
		t.Fatal(err)
	}
	if _, err := readLegacyKeys(context.Background(), sq.DB, nil); err == nil {
		t.Fatal("a key row that cannot be read must not look like an empty table")
	}
	if err := sq.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := readLegacyKeys(context.Background(), sq.DB, nil); err == nil {
		t.Fatal("a closed handle must not look like an empty key table")
	}
}

func sealedKeys(t testing.TB) (map[string]string, func(string) (string, error)) {
	t.Helper()
	raw := make([]byte, sealer.SecretBytes)
	for i := range raw {
		raw[i] = byte(i)
	}
	box, err := sealer.New(raw)
	if err != nil {
		t.Fatal(err)
	}
	const aad = "rllm/v1/apiKeys/key"
	sealed := map[string]string{}
	for k, id := range legacyKeys {
		sealed[box.SealFixed(aad, k)] = id
	}
	return sealed, func(s string) (string, error) { return box.Open(aad, s) }
}

func TestImportMapsRowsAgainstSealedKeys(t *testing.T) {
	sealed, open := sealedKeys(t)
	s := newStore(t)
	rep, err := s.ImportUsage(context.Background(), buildLegacy(t, sampleRows(10), sealed), ImportOptions{OpenKey: open})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Inserted != 10 {
		t.Fatalf("report = %+v", rep)
	}
	var mapped []int64
	if err := s.db.Select(&mapped, `SELECT count(*) FROM "requests" WHERE "apiKeyId" <> ''`); err != nil {
		t.Fatal(err)
	}
	if mapped[0] == 0 {
		t.Fatal("no row was mapped to a key id through the sealed keys")
	}
	for _, v := range storedStrings(t, s) {
		if strings.HasPrefix(v, "enc:v1:") || strings.HasPrefix(v, "sk-") {
			t.Fatal("a key reached the usage rows")
		}
	}
}

func TestImportRefusesSealedKeysWithoutAnOpener(t *testing.T) {
	sealed, _ := sealedKeys(t)
	s := newStore(t)
	_, err := s.ImportUsage(context.Background(), buildLegacy(t, sampleRows(3), sealed), ImportOptions{})
	if !errors.Is(err, ErrSealedKeys) {
		t.Fatalf("err = %v", err)
	}
}

func TestImportFailsWhenASealedKeyDoesNotOpen(t *testing.T) {
	sealed, _ := sealedKeys(t)
	s := newStore(t)
	bad := func(string) (string, error) { return "", errors.New("no") }
	if _, err := s.ImportUsage(context.Background(), buildLegacy(t, sampleRows(3), sealed), ImportOptions{OpenKey: bad}); err == nil {
		t.Fatal("an unopenable key must stop the import")
	}
}

func TestFinishImportReportsAnUnreadableCounter(t *testing.T) {
	src := buildLegacy(t, sampleRows(3), legacyKeys)
	sq := tests.OpenSQLiteWithQuery(t, src, "mode=ro&_pragma=busy_timeout(5000)")
	if err := sq.Close(); err != nil {
		t.Fatal(err)
	}
	s := newStore(t)
	if err := s.finishImport(context.Background(), sq.DB, 0, 0, 3); err == nil {
		t.Fatal("a failed read of the lifetime counter was ignored")
	}
}

func TestFinishImportAcceptsASourceWithoutTheCounterTable(t *testing.T) {
	src := buildLegacy(t, sampleRows(3), legacyKeys)
	if err := writeLegacy(t, src, `DROP TABLE "_meta"`); err != nil {
		t.Fatal(err)
	}
	sq := tests.OpenSQLiteWithQuery(t, src, "mode=ro&_pragma=busy_timeout(5000)")
	defer sq.Close()
	s := newStore(t)
	if err := s.finishImport(context.Background(), sq.DB, 0, 0, 3); err != nil {
		t.Fatalf("a source without _meta must import: %v", err)
	}
}
