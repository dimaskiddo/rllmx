package tests

import (
	"errors"
	"os"
	"strings"
	"testing"

	contractsdb "github.com/goravel/framework/contracts/database/db"
)

type probeRow struct {
	ID   string `db:"id"`
	Data string `db:"data"`
}

func TestNewSQLiteGivesARealWritableConnection(t *testing.T) {
	s := NewSQLite(t)
	if err := s.DB.Statement(`CREATE TABLE "probe" ("id" TEXT PRIMARY KEY, "data" TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Insert(`INSERT INTO "probe" ("id","data") VALUES (?,?)`, "a", "{}"); err != nil {
		t.Fatal(err)
	}
	var rows []probeRow
	if err := s.DB.Select(&rows, `SELECT "id","data" FROM "probe" WHERE "id" = ?`, "a"); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Data != "{}" {
		t.Fatalf("rows = %+v", rows)
	}
	if _, err := os.Stat(s.File); err != nil {
		t.Fatalf("the database file is not on disk: %v", err)
	}
}

func TestEachCallGetsItsOwnDatabase(t *testing.T) {
	a, b := NewSQLite(t), NewSQLite(t)
	if a.Name == b.Name || a.File == b.File {
		t.Fatal("two helpers share a connection name or a file")
	}
	if err := a.DB.Statement(`CREATE TABLE "only_a" ("id" TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err := b.DB.Statement(`SELECT * FROM "only_a"`); err == nil {
		t.Fatal("the second database sees the first one's table")
	}
}

func TestTransactionRollsBackOnError(t *testing.T) {
	s := NewSQLite(t)
	if err := s.DB.Statement(`CREATE TABLE "probe" ("id" TEXT PRIMARY KEY, "data" TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	err := s.DB.Transaction(func(tx contractsdb.Tx) error {
		if _, err := tx.Insert(`INSERT INTO "probe" ("id","data") VALUES (?,?)`, "x", "1"); err != nil {
			return err
		}
		return errProbe
	})
	if err != errProbe {
		t.Fatalf("err = %v", err)
	}
	var rows []probeRow
	if err := s.DB.Select(&rows, `SELECT "id","data" FROM "probe"`); err != nil || len(rows) != 0 {
		t.Fatalf("rows = %+v, err = %v", rows, err)
	}
}

func TestAConnectionToAFileWithSpacesOpens(t *testing.T) {
	dir := t.TempDir() + "/with space"
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	s := OpenSQLite(t, dir+"/data.sqlite")
	if err := s.DB.Statement(`CREATE TABLE "probe" ("id" TEXT)`); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s.File, "with space") {
		t.Fatal("the file path lost its space")
	}
}

var errProbe = errors.New("probe")

func TestSchemaListsTablesColumnsAndIndexes(t *testing.T) {
	s := NewSQLite(t)
	if err := s.Schema.Sql(`CREATE TABLE "probe" ("id" TEXT PRIMARY KEY, "data" TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if err := s.Schema.Sql(`CREATE INDEX "idx_probe_data" ON "probe" ("data")`); err != nil {
		t.Fatal(err)
	}
	tables, err := s.Schema.GetTables()
	if err != nil || len(tables) != 1 || tables[0].Name != "probe" {
		t.Fatalf("tables = %+v, err = %v", tables, err)
	}
	cols, err := s.Schema.GetColumns("probe")
	if err != nil || len(cols) != 2 || cols[0].Name != "id" || cols[1].Name != "data" {
		t.Fatalf("columns = %+v, err = %v", cols, err)
	}
	idx, err := s.Schema.GetIndexes("probe")
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, ix := range idx {
		found = found || ix.Name == "idx_probe_data"
	}
	if !found {
		t.Fatalf("indexes = %+v", idx)
	}
}

func TestReadOnlyConnectionRefusesWrites(t *testing.T) {
	w := NewSQLite(t)
	if err := w.DB.Statement(`CREATE TABLE "probe" ("id" TEXT)`); err != nil {
		t.Fatal(err)
	}
	r := OpenSQLiteReadOnly(t, w.File)
	if err := r.DB.Statement(`INSERT INTO "probe" ("id") VALUES ('x')`); err == nil {
		t.Fatal("the read-only connection accepted a write")
	}
	var ids []string
	if err := r.DB.Select(&ids, `SELECT "id" FROM "probe"`); err != nil {
		t.Fatal(err)
	}
}

func TestCloseReleasesThePool(t *testing.T) {
	s := NewSQLite(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}
