package repository

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rllmx/tests"
)

// fixtureRel is a database written by a real 9router run. It is git-ignored
// because it holds real credentials: the tests copy it and never open it for
// writing, and they skip when it is absent. RLLMX_FIXTURE points at a copy that
// lives elsewhere, which is how the parent checkout's fixture is used.
const (
	fixtureRel = "../../../references/data.sqlite"
	fixtureEnv = "RLLMX_FIXTURE"
)

func fixturePath() string {
	if p := os.Getenv(fixtureEnv); p != "" {
		return p
	}
	return fixtureRel
}

// fixtureCopy copies the fixture into the test's own folder, with any WAL and
// shared-memory file beside it, so the original is never opened for writing.
func fixtureCopy(t *testing.T) string {
	t.Helper()
	tests.RequireSQLite(t)
	src, err := filepath.Abs(fixturePath())
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(src)
	if err != nil || info.Size() < 1<<20 {
		t.Skip("fixture database not present: copy a 9router data.sqlite to references/data.sqlite, or set " + fixtureEnv)
	}
	t.Cleanup(func() { fixtureUnchanged(t, src, info) })

	dir := t.TempDir()
	dst := filepath.Join(dir, "data.sqlite")
	for _, suffix := range []string{"", "-wal", "-shm"} {
		b, err := os.ReadFile(src + suffix)
		if err != nil {
			if suffix == "" {
				t.Fatalf("read the fixture: %v", err)
			}
			continue
		}
		if err := os.WriteFile(dst+suffix, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dst
}

// fixtureUnchanged asserts the original is exactly as it was. A test that wrote
// to it would pass every other assertion and quietly corrupt the one artifact
// this suite cannot regenerate.
func fixtureUnchanged(t *testing.T, src string, before os.FileInfo) {
	t.Helper()
	after, err := os.Stat(src)
	if err != nil {
		t.Errorf("the fixture disappeared: %v", err)
		return
	}
	if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("the fixture changed: %d bytes at %s became %d at %s",
			before.Size(), before.ModTime(), after.Size(), after.ModTime())
	}
}

// fixtureStore opens a copy the way a command does: a store over a writable
// connection, with the migrations registered but not yet run.
func fixtureStore(t *testing.T, path string) *testStore {
	t.Helper()
	sq := tests.OpenSQLite(t, path)
	return &testStore{Store: storeOver(t, sq, tests.Box(t, tests.DBSecret)), SQ: sq}
}

func TestFixtureOpensAndReadsWithoutWriting(t *testing.T) {
	path := fixtureCopy(t)
	before := dumpTables(t, path)

	s := fixtureStore(t, path)
	ctx := context.Background()
	if v, err := s.metaValue(ctx, "schemaVersion"); err != nil || v == "" {
		t.Fatalf("_meta has no schemaVersion: %q %v", v, err)
	}
	populated, err := s.HasConfiguration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !populated {
		t.Fatal("a real database holds no configuration")
	}
	// Every repository reads what it owns, through the same code the server
	// uses, so a column this program does not expect fails here.
	if _, err := s.Connections().List(ctx, ConnFilter{}); err != nil {
		t.Fatalf("connections: %v", err)
	}
	if _, err := s.APIKeys().List(ctx); err != nil {
		t.Fatalf("api keys: %v", err)
	}
	if _, err := s.Nodes().List(ctx, ""); err != nil {
		t.Fatalf("nodes: %v", err)
	}
	if _, err := s.Combos().List(ctx); err != nil {
		t.Fatalf("combos: %v", err)
	}
	if _, err := s.Settings().Get(ctx); err != nil {
		t.Fatalf("settings: %v", err)
	}
	for _, scope := range []string{ScopeModelAliases, ScopeCustomModels, ScopeDisabledModels, ScopePricing} {
		if _, err := s.KV().List(ctx, scope); err != nil {
			t.Fatalf("kv %s: %v", scope, err)
		}
	}
	for _, l := range s.SQ.Log.Lines() {
		for _, leak := range []string{"sk-", "access_token", "refresh_token"} {
			if strings.Contains(l, leak) {
				t.Errorf("a log line carried a stored value: %s", l)
			}
		}
	}
	s.SQ.Close()

	if diff := diffTables(before, dumpTables(t, path)); len(diff) != 0 {
		t.Fatalf("reading changed the database: %v", diff)
	}
}

func TestFixtureRoundTripsASealedRowByteForByte(t *testing.T) {
	path := fixtureCopy(t)
	s := fixtureStore(t, path)
	ctx := context.Background()

	// A pristine 9router database is unsealed, so the first write would seal
	// it and the comparison would prove nothing. Convert first, then check the
	// claim that matters: writing a row's own values back keeps its bytes.
	if _, err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate the fixture copy: %v", err)
	}

	conns, err := s.Connections().List(ctx, ConnFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(conns) == 0 {
		t.Skip("the fixture holds no connections")
	}
	target := conns[0]
	// Connection.Data is the opened view; what went to disk is the sealed
	// column, so the comparison is a dump of the table, narrowed to this row by
	// its id, which the dump carries as the first column.
	before := rowOf(t, path, connTable, target.ID)

	fields := map[string]string{}
	doc := target.Doc()
	for _, k := range doc.Keys() {
		raw, _ := doc.GetRaw(k)
		fields[k] = raw
	}
	if _, err := s.Connections().Update(ctx, target.ID, ConnPatch{Fields: fields}); err != nil {
		t.Fatalf("write %s back: %v", target.ID, err)
	}
	after := rowOf(t, path, connTable, target.ID)

	// Two columns may legitimately move: updatedAt, because a write stamps it,
	// and data, because applying a patch that activates an account clears its
	// stale error state and model locks by design (applyFields). Every other
	// column keeps its bytes, and the id, provider, auth type, priority and
	// timestamps of creation prove the row was rewritten in place.
	diff := rowDiff(before, after)
	for _, col := range diff {
		if col != "updatedAt" && col != "data" {
			t.Fatalf("writing a row's own values back changed column %q", col)
		}
	}
	if len(diff) == 0 {
		t.Fatal("the write changed nothing at all, so nothing was exercised")
	}
}

// connTable is the table ConnectionsRepo owns.
const connTable = "providerConnections"

// rowOf returns the dump line of one row, matched by its id.
func rowOf(t *testing.T, path, table, id string) []string {
	t.Helper()
	for _, line := range dumpTables(t, path)[table] {
		// The id column is TEXT, so its digest starts with "text:" and the
		// hex of the id follows.
		if strings.HasPrefix(line, "text:"+hex(id)+"|") {
			return []string{line}
		}
	}
	t.Fatalf("row %s is not in the dump", id)
	return nil
}

func hex(s string) string {
	const digits = "0123456789ABCDEF"
	var b strings.Builder
	for _, c := range []byte(s) {
		b.WriteByte(digits[c>>4])
		b.WriteByte(digits[c&0x0f])
	}
	return b.String()
}

// rowDiff names the columns whose value changed between two one-row dumps. The
// dump encodes each row as typeof:hex per column joined by '|', so a column
// index is a position in that string.
func rowDiff(before, after []string) []string {
	if len(before) != 1 || len(after) != 1 {
		return []string{"row count changed"}
	}
	a, b := strings.Split(before[0], "|"), strings.Split(after[0], "|")
	if len(a) != len(b) {
		return []string{"column count changed"}
	}
	var changed []string
	for i := range a {
		if a[i] != b[i] {
			changed = append(changed, connColumns[i])
		}
	}
	return changed
}

// connColumns is the column order ConnectionsRepo selects, which the dump
// follows.
var connColumns = []string{"id", "provider", "authType", "name", "email", "priority", "isActive", "data", "createdAt", "updatedAt"}

// TestFixtureSealsEverythingOnMigration pins what Migrate actually does to a
// real database: every plain secret is converted, and no value is left readable.
func TestFixtureSealsEverythingOnMigration(t *testing.T) {
	path := fixtureCopy(t)
	s := fixtureStore(t, path)
	ctx := context.Background()

	before, err := s.scanSecrets(s.on(ctx), false)
	if err != nil {
		t.Fatal(err)
	}
	if before.values() == 0 {
		t.Fatal("the fixture is documented as unsealed, but no plain secret was found")
	}
	if _, err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := s.scanSecrets(s.on(ctx), false)
	if err != nil {
		t.Fatal(err)
	}
	if after.values() != 0 || after.unopenable != 0 {
		t.Fatalf("after migration %d plain values and %d unopenable ones remain", after.values(), after.unopenable)
	}
	// The converted database is the one a later command must accept.
	if err := s.Verify(ctx); err != nil {
		t.Fatalf("a just-migrated database fails its own guard: %v", err)
	}
}

func TestFixtureSatisfiesTheAdditivePlan(t *testing.T) {
	path := fixtureCopy(t)
	s := fixtureStore(t, path)

	p, err := s.computePlan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !p.empty() {
		t.Fatalf("a real database does not match this program's schema: %d tables, %d columns, %d indexes missing",
			len(p.tables), len(p.columns), len(p.indexes))
	}
}

func TestFixtureHoldsPlainSecretsBeforeMigration(t *testing.T) {
	path := fixtureCopy(t)
	s := fixtureStore(t, path)
	sp, err := s.scanSecrets(s.on(context.Background()), false)
	if err != nil {
		t.Fatal(err)
	}
	if sp.values() == 0 {
		t.Fatal("the fixture is documented as unsealed, but no plain secret was found")
	}
}
