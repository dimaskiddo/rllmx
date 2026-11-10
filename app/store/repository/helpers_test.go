package repository

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	contractsdb "github.com/goravel/framework/contracts/database/db"
	contractsschema "github.com/goravel/framework/contracts/database/schema"
	"github.com/goravel/framework/database/migration"

	"rllmx/app/store/dialect"
	"rllmx/app/store/document"
	"rllmx/app/store/sealer"
	"rllmx/tests"
)

// storeOver builds a Store on a connection the test already opened. The
// migrations are registered once on that connection's Schema, as the
// application registers them with WithMigrations.
func storeOver(t testing.TB, sq *tests.Conn, box *sealer.Box) *Store {
	t.Helper()
	d := tests.Engine()
	sq.Schema.Register(Migrations(func() contractsschema.Schema { return sq.Schema }, d))
	backup := ""
	if sq.File != "" {
		backup = filepath.Join(filepath.Dir(sq.File), "backups")
	}
	s, err := New(Options{
		DB: sq.DB, Schema: sq.Schema, Log: sq.Log, Box: box, Dialect: d,
		PGSchema:   sq.PGSchema,
		Migrate:    func() error { return migration.NewMigrator(nil, sq.Schema, "migrations").Run() },
		BackupDir:  backup,
		AppVersion: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

type testStore struct {
	*Store
	SQ *tests.Conn
}

// newMigrated opens a fresh database and migrates it.
func newMigrated(t testing.TB) *testStore {
	t.Helper()
	sq := tests.OpenAny(t)
	s := storeOver(t, sq, tests.Box(t, tests.DBSecret))
	if _, err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return &testStore{Store: s, SQ: sq}
}

// reopen opens the same dialect.SQLite file again with a new connection, as a restart
// does. It is dialect.SQLite only: on PostgreSQL reopening is a new schema, not a file.
func reopen(t testing.TB, file, secret string) *testStore {
	t.Helper()
	sq := tests.OpenSQLite(t, file)
	var box *sealer.Box
	if secret != "" {
		box = tests.Box(t, secret)
	}
	return &testStore{Store: storeOver(t, sq, box), SQ: sq}
}

func (s *testStore) exec(t testing.TB, query string, args ...any) {
	t.Helper()
	if err := s.SQ.DB.Statement(query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

func (s *testStore) strings(t testing.TB, query string, args ...any) []string {
	t.Helper()
	var out []string
	if err := s.SQ.DB.Select(&out, query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return out
}

func (s *testStore) one(t testing.TB, query string, args ...any) string {
	t.Helper()
	rows := s.strings(t, query, args...)
	if len(rows) != 1 {
		t.Fatalf("%s returned %d rows", query, len(rows))
	}
	return rows[0]
}

// hasTable answers through the store, which qualifies the name per dialect, so
// a catalog assertion reads the same on both engines.
func (s *testStore) hasTable(name string) bool { return s.Store.hasTable(name) }

// hasIndex reports whether the table carries an index of that name.
func (s *testStore) hasIndex(t testing.TB, table, index string) bool {
	t.Helper()
	idx, err := s.SQ.Schema.GetIndexes(table)
	if err != nil {
		t.Fatalf("indexes of %s: %v", table, err)
	}
	for _, ix := range idx {
		if ix.Name == index {
			return true
		}
	}
	return false
}

// dumpTables captures every row of every table as hex text per column, so a
// comparison sees changes of type and bytes, plus the DDL itself under the
// "<schema>" key so a change to a table's definition is caught too. It reads
// through its own read-only connection.
func dumpTables(t testing.TB, file string) map[string][]string {
	t.Helper()
	ro := tests.OpenSQLiteReadOnly(t, file)
	var names []string
	if err := ro.DB.Select(&names, `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`); err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	var ddl []string
	if err := ro.DB.Select(&ddl, `SELECT sql FROM sqlite_master WHERE sql IS NOT NULL AND name NOT LIKE 'sqlite_%' ORDER BY name`); err != nil {
		t.Fatal(err)
	}
	sort.Strings(ddl)
	out["<schema>"] = ddl
	for _, tb := range names {
		var cols []string
		if err := ro.DB.Select(&cols, `SELECT name FROM pragma_table_info(?)`, tb); err != nil {
			t.Fatal(err)
		}
		exprs := make([]string, len(cols))
		for i, c := range cols {
			exprs[i] = fmt.Sprintf(`typeof("%[1]s")||':'||hex("%[1]s")`, c)
		}
		var rows []string
		if len(exprs) > 0 {
			if err := ro.DB.Select(&rows, `SELECT `+strings.Join(exprs, `||'|'||`)+` FROM "`+tb+`"`); err != nil {
				t.Fatal(err)
			}
		}
		sort.Strings(rows)
		out[tb] = rows
	}
	return out
}

// diffTables compares two dumps in both directions, so a table or row that
// appeared in only one of them is reported too.
func diffTables(a, b map[string][]string) []string {
	var diff []string
	names := map[string]bool{}
	for name := range a {
		names[name] = true
	}
	for name := range b {
		names[name] = true
	}
	for name := range names {
		rows, other := a[name], b[name]
		count := map[string]int{}
		for _, d := range rows {
			count[d]++
		}
		for _, d := range other {
			count[d]--
		}
		var gone, added int
		for _, n := range count {
			if n > 0 {
				gone += n
			} else {
				added -= n
			}
		}
		switch {
		case gone == 0 && added == 0:
		case len(rows) != len(other):
			diff = append(diff, fmt.Sprintf("%s: %d rows -> %d rows", name, len(rows), len(other)))
		default:
			diff = append(diff, fmt.Sprintf("%s: %d rows differ", name, gone))
		}
	}
	sort.Strings(diff)
	return diff
}

func backupNames(t testing.TB, file string) []string {
	t.Helper()
	entries, _ := os.ReadDir(filepath.Join(filepath.Dir(file), "backups"))
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// testOpener opens another dialect.SQLite file through the same helper the stores use,
// the way the data provider will open one through a configured connection.
func testOpener(t testing.TB) Opener {
	t.Helper()
	return func(path, query string) (*Conn, error) {
		sq := tests.OpenSQLiteWithQuery(t, path, query)
		return &Conn{DB: sq.DB, Schema: sq.Schema, Close: sq.Close}, nil
	}
}

// reopenReadOnly is the read-only open the terminal commands use.
func reopenReadOnly(t testing.TB, file string) *testStore {
	t.Helper()
	sq := tests.OpenSQLiteReadOnly(t, file)
	s, err := New(Options{DB: sq.DB, Schema: sq.Schema, Log: sq.Log, Dialect: dialect.SQLite, AppVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return &testStore{Store: s, SQ: sq}
}

func mkConn(t testing.TB, s *testStore, in ConnInput) Connection {
	t.Helper()
	c, err := s.Connections().Create(context.Background(), in, nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return c
}

func mkNode(t testing.TB, s *testStore, id, typ, data string) Node {
	t.Helper()
	n, err := s.Nodes().CreateUnique(context.Background(), Node{ID: id, Type: typ, Name: id, Data: data}, "")
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// putCustomModel stores a custom model row under the key format other readers
// of the table expect: provider alias, id and type joined by "|".
func putCustomModel(t testing.TB, s *testStore, alias, id string) {
	t.Helper()
	val := `{"providerAlias":"` + alias + `","id":"` + id + `","type":"llm","name":"` + id + `"}`
	if err := s.KV().Set(context.Background(), ScopeCustomModels, alias+"|"+id+"|llm", val); err != nil {
		t.Fatal(err)
	}
}

func lockAt(s *testStore, id, model string, until time.Time, status int, reason string, bump int) error {
	_, err := s.Connections().LockFromLevel(context.Background(), id, model, status, reason, func(level int) (time.Time, int, bool) {
		return until, level + bump, true
	})
	return err
}

func mustLock(t testing.TB, s *testStore, id, model string, until time.Time, status int, reason string, bump int) {
	t.Helper()
	if err := lockAt(s, id, model, until, status, reason, bump); err != nil {
		t.Fatalf("lock %q: %v", model, err)
	}
}

// setRaw stores raw JSON fields on a connection without the side effects of
// activating it, which is how a test builds a row in an odd state.
func setRaw(t testing.TB, s *testStore, id string, fields map[string]string) {
	t.Helper()
	err := s.Tx(context.Background(), func(tx contractsdb.Tx) error {
		c, d, err := s.Connections().lockRow(tx, id)
		if err != nil {
			return err
		}
		for k, v := range fields {
			if err := d.SetRaw(k, v); err != nil {
				return err
			}
		}
		_, err = s.Connections().writeData(tx, c, d)
		return err
	})
	if err != nil {
		t.Fatalf("set raw: %v", err)
	}
}

func mustTime(t testing.TB, s string) time.Time {
	t.Helper()
	tm, err := dialect.ParseTimestamp(s)
	if err != nil {
		t.Fatal(err)
	}
	return tm
}

func mustDoc(t testing.TB, raw string) document.Doc {
	t.Helper()
	d, err := document.ParseDoc(raw)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func lockCleared(d document.Doc, key string) bool { return !d.Has(key) || d.IsNull(key) }

func psdOfConn(t testing.TB, s *testStore, id string) document.Doc {
	t.Helper()
	c, ok, err := s.Connections().Get(context.Background(), id)
	if err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	raw, _ := c.Doc().GetRaw("providerSpecificData")
	return mustDoc(t, raw)
}
