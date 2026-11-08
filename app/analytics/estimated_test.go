package analytics

import (
	"context"
	"os"
	"path/filepath"
	"rllmx/app/dialect"
	"testing"
	"time"
)

func hasEstimatedColumn(t testing.TB, s *testStore) bool {
	t.Helper()
	query := `SELECT count(*) FROM pragma_table_info('requests') WHERE name = 'estimated'`
	if s.Dialect() == dialect.Postgres {
		query = `SELECT count(*) FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'requests' AND column_name = 'estimated'`
	}
	var n []int64
	err := s.db.Select(&n, query)
	if err != nil {
		t.Fatal(err)
	}
	return len(n) == 1 && n[0] == 1
}

func backupDirs(t testing.TB, file string) []string {
	t.Helper()
	dir := filepath.Join(filepath.Dir(file), backupDirName)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func TestEstimatedRoundTripsThroughTheWriter(t *testing.T) {
	s := newStore(t)
	w := s.NewWriter(WriterOptions{})
	a, b := rec(1, t0), rec(2, t0.Add(time.Second))
	a.Estimated = true
	w.Enqueue(a)
	w.Enqueue(b)
	if err := w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		RequestID string `db:"requestId"`
		Estimated int64  `db:"estimated"`
	}
	if err := s.db.Select(&rows, `SELECT "requestId","estimated" FROM "requests"`); err != nil {
		t.Fatal(err)
	}
	flags := map[string]bool{}
	for _, r := range rows {
		flags[r.RequestID] = r.Estimated == 1
	}
	if len(flags) != 2 || !flags["r1"] || flags["r2"] {
		t.Fatalf("estimated flags: %v", flags)
	}
}

func TestFreshDatabaseHasTheColumnAndTakesNoBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db", "analytics.sqlite")
	s := openAt(t, path)
	if !hasEstimatedColumn(t, s) {
		t.Fatal("a new database must have the estimated column")
	}
	if b := backupDirs(t, path); len(b) != 0 {
		t.Fatalf("a fresh database needs no backup: %v", b)
	}
}

func TestOldDatabaseGainsTheColumnAfterABackupAndKeepsItsRows(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "db", "analytics.sqlite")
	s := openAt(t, path)
	w := s.NewWriter(WriterOptions{})
	for i := range 5 {
		w.Enqueue(rec(i, t0.Add(time.Duration(i)*time.Minute)))
	}
	if err := w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Statement(`ALTER TABLE "requests" DROP COLUMN "estimated"`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	s2 := openAt(t, path)
	if !hasEstimatedColumn(t, s2) {
		t.Fatal("the column was not added")
	}
	if got := count(t, s2, "requests"); got != 5 {
		t.Fatalf("rows after migration: %d", got)
	}
	var estimated []int64
	if err := s2.db.Select(&estimated, `SELECT count(*) FROM "requests" WHERE "estimated" <> 0`); err != nil {
		t.Fatal(err)
	}
	if estimated[0] != 0 {
		t.Fatalf("existing rows default to reported, got %d estimated", estimated[0])
	}
	b := backupDirs(t, path)
	if len(b) != 1 {
		t.Fatalf("expected exactly one backup, got %v", b)
	}
	fi, err := os.Stat(filepath.Join(filepath.Dir(path), backupDirName, b[0]))
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("backup dir mode: %v %v", fi, err)
	}
}

func TestOpeningACurrentDatabaseAgainWritesAndBacksUpNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db", "analytics.sqlite")
	s := openAt(t, path)
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = openAt(t, path)
	if b := backupDirs(t, path); len(b) != 0 {
		t.Fatalf("no backup expected: %v", b)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != before.Size() {
		t.Fatalf("size changed %d -> %d", before.Size(), after.Size())
	}
}

func TestFailedBackupStopsTheMigration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "db", "analytics.sqlite")
	s := openAt(t, path)
	if err := s.db.Statement(`ALTER TABLE "requests" DROP COLUMN "estimated"`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "db", backupDirName), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), Options{
		DB: s.sq.DB, Schema: s.sq.Schema, Log: s.sq.Log, Dialect: dialect.SQLite,
		File: path, Version: "test", Open: testOpener(t),
	}); err == nil {
		t.Fatal("a failed backup must abort the change")
	}
	if err := os.Remove(filepath.Join(dir, "db", backupDirName)); err != nil {
		t.Fatal(err)
	}
	s3 := openAt(t, path)
	if !hasEstimatedColumn(t, s3) {
		t.Fatal("the migration must succeed once the backup can be written")
	}
}
