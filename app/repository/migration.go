package repository

import (
	"errors"
	"fmt"

	contractsschema "github.com/goravel/framework/contracts/database/schema"
	"rllmx/app/dialect"
)

// ErrForwardOnly is what Down returns: the schema only ever grows, and no
// command that would roll it back is registered.
var ErrForwardOnly = errors.New("repository: migrations are forward-only")

// Migrations returns the framework migrations that create the 9router tables,
// one per table, and add the columns an older database may lack, one per
// column. Each one checks what exists before it writes, so running them on a
// 9router database that already has everything changes nothing. The DDL is
// written with Schema().Sql: the framework's blueprint would make varchar
// columns and drop the CHECK, UNIQUE and named indexes.
//
// sch is a function because the migrations are declared before the Schema
// facade exists and only run later.
func Migrations(sch func() contractsschema.Schema, d dialect.Dialect) []contractsschema.Migration {
	var out []contractsschema.Migration
	for _, tb := range tables {
		out = append(out, &createTable{sch: sch, d: d, table: tb, n: tb.Seq})
	}
	for _, tb := range tables {
		for _, c := range tb.Columns {
			if c.Added {
				out = append(out, &addColumn{sch: sch, d: d, table: tb, col: c})
			}
		}
	}
	return out
}

type createTable struct {
	sch   func() contractsschema.Schema
	d     dialect.Dialect
	table Table
	n     int
}

func (m *createTable) Signature() string {
	return fmt.Sprintf("20260101_%06d_create_%s_table", m.n, m.table.Name)
}

func (m *createTable) Up() error {
	s := m.sch()
	if err := s.Sql(createTableSQL(m.table, m.d)); err != nil {
		return fmt.Errorf("repository: create table %s: %w", m.table.Name, err)
	}
	for _, ix := range m.table.Indexes {
		if err := s.Sql(createIndexSQL(m.table, ix)); err != nil {
			return fmt.Errorf("repository: create index %s: %w", ix.Name, err)
		}
	}
	return nil
}

func (m *createTable) Down() error { return ErrForwardOnly }

type addColumn struct {
	sch   func() contractsschema.Schema
	d     dialect.Dialect
	table Table
	col   Column
}

func (m *addColumn) Signature() string {
	return fmt.Sprintf("20260102_000000_add_%s_%s", m.table.Name, m.col.Name)
}

func (m *addColumn) Up() error {
	s := m.sch()
	cols, err := s.GetColumns(m.table.Name)
	if err != nil {
		return fmt.Errorf("repository: read columns of %s: %w", m.table.Name, err)
	}
	for _, c := range cols {
		if c.Name == m.col.Name {
			return nil
		}
	}
	if err := s.Sql(addColumnSQL(m.table, m.col, m.d)); err != nil {
		return fmt.Errorf("repository: add column %s.%s: %w", m.table.Name, m.col.Name, err)
	}
	return nil
}

func (m *addColumn) Down() error { return ErrForwardOnly }
