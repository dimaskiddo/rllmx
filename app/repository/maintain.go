package repository

import (
	"context"
	"fmt"
	"rllmx/app/dialect"
)

// Optimize lets dialect.SQLite refresh its query statistics. PostgreSQL has autovacuum,
// so it does nothing there.
func (s *Store) Optimize(ctx context.Context) error {
	if s.dialect != dialect.SQLite {
		return nil
	}
	return s.on(ctx).Statement(`PRAGMA optimize`)
}

// Checkpoint folds the dialect.SQLite WAL into the database file and truncates it.
func (s *Store) Checkpoint(ctx context.Context) error {
	if s.dialect != dialect.SQLite {
		return nil
	}
	var res []struct {
		Busy         int `db:"busy"`
		Log          int `db:"log"`
		Checkpointed int `db:"checkpointed"`
	}
	if err := s.on(ctx).Select(&res, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return err
	}
	if len(res) == 1 && res[0].Busy != 0 {
		return fmt.Errorf("repository: the WAL could not be truncated: another connection is reading")
	}
	return nil
}

// Vacuum rebuilds the database file to return free pages. It needs exclusive
// use of the file, so the caller holds the instance lock.
func (s *Store) Vacuum(ctx context.Context) error {
	return s.on(ctx).Statement(`VACUUM`)
}
