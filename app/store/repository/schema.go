package repository

import (
	"fmt"
	"rllmx/app/store/dialect"
	"strings"
)

type colType int

const (
	tText colType = iota
	tInt
	tReal
)

// Column describes one column. Added marks a column that may be missing from
// an existing database and is then added; such a column must be nullable or
// have a default, because ALTER TABLE cannot add anything else.
type Column struct {
	Name    string
	Type    colType
	NotNull bool
	Unique  bool
	PK      bool
	Default string
	Check   string
	Added   bool
}

// Index is a table index in the expected schema.
type Index struct {
	Name    string
	Columns []string
}

// Table describes one table. PKColumns is set for a composite primary key.
type Table struct {
	// Seq is the table's number in its migration name. It is fixed, not the
	// position in the list, so removing a table never renumbers the others and an
	// existing database keeps recognising the migrations it already ran.
	Seq       int
	Name      string
	Columns   []Column
	PKColumns []string
	Indexes   []Index
}

func txt(n string) Column     { return Column{Name: n, Type: tText} }
func txtNN(n string) Column   { return Column{Name: n, Type: tText, NotNull: true} }
func integer(n string) Column { return Column{Name: n, Type: tInt} }
func added(c Column) Column {
	c.Added = true
	return c
}

func intDef(n, def string) Column {
	return Column{Name: n, Type: tInt, Default: def}
}

// tables is the compatibility contract: the tables, columns and index names
// 9router creates that this program uses. Sequence number 5 belonged to a table
// this program no longer creates; an existing database that has it keeps it. The usage tables are not here because
// usage lives in the analytics store. The DDL is written by createTableSQL
// through Schema().Sql: the framework's blueprint would make varchar columns
// and drop the CHECK, UNIQUE and named indexes.
var tables = []Table{
	{Seq: 1, Name: "_meta", Columns: []Column{{Name: "key", Type: tText, PK: true}, txtNN("value")}},
	{Seq: 2, Name: "settings", Columns: []Column{{Name: "id", Type: tInt, PK: true, Check: `"id" = 1`}, txtNN("data")}},
	{Seq: 3, Name: "providerConnections", Columns: []Column{
		{Name: "id", Type: tText, PK: true}, txtNN("provider"), txtNN("authType"), txt("name"), txt("email"),
		integer("priority"), intDef("isActive", "1"), txtNN("data"), txtNN("createdAt"), txtNN("updatedAt"),
	}, Indexes: []Index{
		{"idx_pc_provider", []string{"provider"}},
		{"idx_pc_provider_active", []string{"provider", "isActive"}},
		{"idx_pc_priority", []string{"provider", "priority"}},
	}},
	{Seq: 4, Name: "providerNodes", Columns: []Column{
		{Name: "id", Type: tText, PK: true}, txt("type"), txt("name"), txtNN("data"), txtNN("createdAt"), txtNN("updatedAt"),
	}, Indexes: []Index{{"idx_pn_type", []string{"type"}}}},
	{Seq: 6, Name: "apiKeys", Columns: []Column{
		{Name: "id", Type: tText, PK: true}, {Name: "key", Type: tText, NotNull: true, Unique: true}, txt("name"), txt("machineId"),
		intDef("isActive", "1"), txtNN("createdAt"), added(intDef("accessRestricted", "0")), added(txt("accessAllow")),
	}, Indexes: []Index{{"idx_ak_key", []string{"key"}}}},
	{Seq: 7, Name: "combos", Columns: []Column{
		{Name: "id", Type: tText, PK: true}, {Name: "name", Type: tText, NotNull: true, Unique: true}, txt("kind"), txtNN("models"), txtNN("createdAt"), txtNN("updatedAt"),
	}, Indexes: []Index{{"idx_combo_name", []string{"name"}}}},
	{Seq: 8, Name: "kv", Columns: []Column{txtNN("scope"), txtNN("key"), txtNN("value")}, PKColumns: []string{"scope", "key"},
		Indexes: []Index{{"idx_kv_scope", []string{"scope"}}}},
}

// validateTables rejects an additive column that ALTER TABLE could not add.
func validateTables(ts []Table) error {
	for _, tb := range ts {
		for _, c := range tb.Columns {
			if c.Added && c.NotNull && c.Default == "" {
				return fmt.Errorf("repository: column %s.%s is NOT NULL without a default and cannot be added to an existing table", tb.Name, c.Name)
			}
		}
	}
	return nil
}

func quote(id string) string { return `"` + strings.ReplaceAll(id, `"`, `""`) + `"` }

func sqlType(t colType, d dialect.Dialect) string {
	switch t {
	case tInt:
		if d == dialect.Postgres {
			return "BIGINT"
		}
		return "INTEGER"
	case tReal:
		if d == dialect.Postgres {
			return "DOUBLE PRECISION"
		}
		return "REAL"
	}
	return "TEXT"
}

func columnSQL(c Column, d dialect.Dialect, inline bool) string {
	var b strings.Builder
	b.WriteString(quote(c.Name))
	b.WriteByte(' ')
	b.WriteString(sqlType(c.Type, d))
	if c.PK && inline {
		b.WriteString(" PRIMARY KEY")
	}
	if c.NotNull {
		b.WriteString(" NOT NULL")
	}
	if c.Unique {
		b.WriteString(" UNIQUE")
	}
	if c.Default != "" {
		b.WriteString(" DEFAULT ")
		b.WriteString(c.Default)
	}
	if c.Check != "" {
		b.WriteString(" CHECK (")
		b.WriteString(c.Check)
		b.WriteByte(')')
	}
	return b.String()
}

func createTableSQL(t Table, d dialect.Dialect) string {
	parts := make([]string, 0, len(t.Columns)+1)
	for _, c := range t.Columns {
		parts = append(parts, columnSQL(c, d, len(t.PKColumns) == 0))
	}
	if len(t.PKColumns) > 0 {
		cols := make([]string, len(t.PKColumns))
		for i, c := range t.PKColumns {
			cols[i] = quote(c)
		}
		parts = append(parts, "PRIMARY KEY ("+strings.Join(cols, ", ")+")")
	}
	return "CREATE TABLE IF NOT EXISTS " + quote(t.Name) + " (" + strings.Join(parts, ", ") + ")"
}

func createIndexSQL(t Table, ix Index) string {
	cols := make([]string, len(ix.Columns))
	for i, c := range ix.Columns {
		cols[i] = quote(c)
	}
	return "CREATE INDEX IF NOT EXISTS " + quote(ix.Name) + " ON " + quote(t.Name) + " (" + strings.Join(cols, ", ") + ")"
}

func addColumnSQL(t Table, c Column, d dialect.Dialect) string {
	stmt := "ALTER TABLE " + quote(t.Name) + " ADD COLUMN "
	if d == dialect.Postgres {
		stmt += "IF NOT EXISTS "
	}
	return stmt + columnSQL(c, d, false)
}
