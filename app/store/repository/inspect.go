package repository

import (
	"context"

	contractsdb "github.com/goravel/framework/contracts/database/db"
)

// TableExists asks the dialect.SQLite catalog of a database this store does not own,
// such as a legacy source. A query error is returned, never read as "missing".
func TableExists(ctx context.Context, db contractsdb.DB, name string) (bool, error) {
	var found []string
	err := db.WithContext(ctx).Select(&found, `SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, name)
	if err != nil {
		return false, err
	}
	return len(found) > 0, nil
}

// ColumnExists reports whether a table of a legacy source has a column. The
// older program added columns over time, so a source may lack the newest ones.
func ColumnExists(ctx context.Context, db contractsdb.DB, table, column string) (bool, error) {
	var found []string
	err := db.WithContext(ctx).Select(&found, `SELECT name FROM pragma_table_info(?) WHERE name = ?`, table, column)
	if err != nil {
		return false, err
	}
	return len(found) > 0, nil
}
