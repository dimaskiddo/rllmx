package repository

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"

	contractsdb "github.com/goravel/framework/contracts/database/db"
	"rllmx/app/store/dialect"
)

var (
	// ErrNotFound means the row a write targeted does not exist.
	ErrNotFound = errors.New("repository: not found")
	// ErrProtectedSetting means a settings patch named a key that has its own writer.
	ErrProtectedSetting = errors.New("repository: protected setting")
)

// ErrNameConflict reports an API-key connection whose name is already taken.
type ErrNameConflict struct {
	ExistingID   string
	ExistingName string
}

func (e *ErrNameConflict) Error() string {
	return fmt.Sprintf("repository: a connection named %q already exists", e.ExistingName)
}

// affected runs a delete and returns how many rows it removed. The DB facade
// reports that only for Insert, Update and Delete, not for Statement.
func (s *Store) affected(x contractsdb.Tx, query string, args ...any) (int64, error) {
	res, err := x.Delete(dialect.Rebind(query, s.dialect), args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected, nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func nullStr(n sql.NullString) string {
	if n.Valid {
		return n.String
	}
	return ""
}
