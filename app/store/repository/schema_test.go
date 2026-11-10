package repository

import (
	"strings"
	"testing"

	"rllmx/app/store/dialect"
	"rllmx/tests"
)

func TestSchemaCoversTheCompatibilityTables(t *testing.T) {
	want := []string{"_meta", "settings", "providerConnections", "providerNodes", "apiKeys", "combos", "kv"}
	var got []string
	for _, tb := range tables {
		got = append(got, tb.Name)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("tables = %v, want %v", got, want)
	}
	for _, tb := range tables {
		switch tb.Name {
		case "usageHistory", "usageDaily", "requestDetails", "proxyPoolFitness", "proxyPools":
			t.Errorf("table %s must not be created by rllmx", tb.Name)
		}
	}
}

func TestSchemaIndexNamesMatchTheStoredNames(t *testing.T) {
	want := map[string]bool{
		"idx_pc_provider": true, "idx_pc_provider_active": true, "idx_pc_priority": true, "idx_pn_type": true,
		"idx_ak_key": true, "idx_combo_name": true, "idx_kv_scope": true,
	}
	got := map[string]bool{}
	for _, tb := range tables {
		for _, ix := range tb.Indexes {
			got[ix.Name] = true
		}
	}
	for n := range want {
		if !got[n] {
			t.Errorf("index %s missing", n)
		}
	}
	for n := range got {
		if !want[n] {
			t.Errorf("unexpected index %s", n)
		}
	}
}

func TestAddedColumnsMustBeNullableOrDefaulted(t *testing.T) {
	bad := []Table{{Name: "x", Columns: []Column{{Name: "id", Type: tText, PK: true}, {Name: "c", Type: tText, NotNull: true, Added: true}}}}
	if err := validateTables(bad); err == nil {
		t.Fatal("a NOT NULL column without a default was accepted for additive sync")
	}
	if err := validateTables(tables); err != nil {
		t.Fatalf("the real schema fails its own check: %v", err)
	}
}

func TestCreateStatementsPerDialect(t *testing.T) {
	for _, tb := range tables {
		sq := createTableSQL(tb, dialect.SQLite)
		pg := createTableSQL(tb, dialect.Postgres)
		if !strings.Contains(sq, `"`+tb.Name+`"`) || !strings.Contains(pg, `"`+tb.Name+`"`) {
			t.Errorf("%s: identifier not quoted", tb.Name)
		}
		if strings.Contains(pg, "AUTOINCREMENT") || strings.Contains(sq, "BIGSERIAL") {
			t.Errorf("%s: wrong dialect keywords", tb.Name)
		}
		if strings.Contains(strings.ToUpper(sq), "TIMESTAMP") || strings.Contains(strings.ToUpper(sq), "DATETIME") {
			t.Errorf("%s: timestamp columns must be TEXT", tb.Name)
		}
		if strings.Contains(strings.ToUpper(sq), "VARCHAR") {
			t.Errorf("%s: the stored columns are TEXT, not the framework's varchar", tb.Name)
		}
	}
	if sq := createTableSQL(find(t, "settings"), dialect.SQLite); !strings.Contains(sq, "CHECK") {
		t.Errorf("settings lost its CHECK (id = 1): %s", sq)
	}
	if pg := createTableSQL(find(t, "apiKeys"), dialect.Postgres); !strings.Contains(pg, `"key" TEXT NOT NULL UNIQUE`) {
		t.Errorf("apiKeys.key must be UNIQUE: %s", pg)
	}
	if pg := createTableSQL(find(t, "providerConnections"), dialect.Postgres); !strings.Contains(pg, `"priority" BIGINT`) {
		t.Errorf("priority must be BIGINT on PostgreSQL: %s", pg)
	}
}

func TestCreateTableSQLIsTheStoredDDL(t *testing.T) {
	got := createTableSQL(find(t, "kv"), dialect.SQLite)
	const want = `CREATE TABLE IF NOT EXISTS "kv" ("scope" TEXT NOT NULL, "key" TEXT NOT NULL, "value" TEXT NOT NULL, PRIMARY KEY ("scope", "key"))`
	if got != want {
		t.Fatalf("kv DDL:\n got %s\nwant %s", got, want)
	}
	got = createTableSQL(find(t, "settings"), dialect.SQLite)
	const wantSettings = `CREATE TABLE IF NOT EXISTS "settings" ("id" INTEGER PRIMARY KEY CHECK ("id" = 1), "data" TEXT NOT NULL)`
	if got != wantSettings {
		t.Fatalf("settings DDL:\n got %s\nwant %s", got, wantSettings)
	}
	idx := createIndexSQL(find(t, "providerConnections"), find(t, "providerConnections").Indexes[1])
	const wantIdx = `CREATE INDEX IF NOT EXISTS "idx_pc_provider_active" ON "providerConnections" ("provider", "isActive")`
	if idx != wantIdx {
		t.Fatalf("index DDL:\n got %s\nwant %s", idx, wantIdx)
	}
}

func TestQuoteDoublesEmbeddedQuotes(t *testing.T) {
	if got := quote(`a"b`); got != `"a""b"` {
		t.Fatalf("quote = %s", got)
	}
}

func find(t *testing.T, name string) Table {
	t.Helper()
	for _, tb := range tables {
		if tb.Name == name {
			return tb
		}
	}
	t.Fatalf("no table %s", name)
	return Table{}
}

// An added column is checked by the migration through the Schema facade, whose
// dialect.SQLite column reader fails on a composite primary key.
func TestNoAddedColumnLivesInATableWithACompositeKey(t *testing.T) {
	for _, tb := range tables {
		if len(tb.PKColumns) == 0 {
			continue
		}
		for _, c := range tb.Columns {
			if c.Added {
				t.Errorf("%s.%s is added to a table with a composite key", tb.Name, c.Name)
			}
		}
	}
}

func TestMigrationNamesKeepTheirNumbersWhenATableIsRemoved(t *testing.T) {
	want := map[string]string{
		"_meta": "20260101_000001_create__meta_table", "settings": "20260101_000002_create_settings_table",
		"providerConnections": "20260101_000003_create_providerConnections_table", "providerNodes": "20260101_000004_create_providerNodes_table",
		"apiKeys": "20260101_000006_create_apiKeys_table", "combos": "20260101_000007_create_combos_table", "kv": "20260101_000008_create_kv_table",
	}
	got := map[string]string{}
	for _, m := range Migrations(nil, tests.Engine()) {
		if c, ok := m.(*createTable); ok {
			got[c.table.Name] = c.Signature()
		}
	}
	for table, sig := range want {
		if got[table] != sig {
			t.Errorf("%s: %q, want %q: a database that ran it would run it again", table, got[table], sig)
		}
	}
	if len(got) != len(want) {
		t.Errorf("%d create migrations, want %d", len(got), len(want))
	}
}
