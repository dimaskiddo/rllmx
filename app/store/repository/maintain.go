package repository

import (
	"context"
	"fmt"
	"rllmx/app/store/dialect"
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

// Analyze refreshes the planner's statistics. SQLite samples a bounded number
// of rows, so the pass stays short on a large usage file.
func (s *Store) Analyze(ctx context.Context) error {
	if s.dialect != dialect.SQLite {
		return s.on(ctx).Statement(`ANALYZE`)
	}
	if err := s.on(ctx).Statement(`PRAGMA analysis_limit=400`); err != nil {
		return err
	}
	return s.on(ctx).Statement(`ANALYZE`)
}

// IncrementalVacuum returns free pages when the file was built with
// auto_vacuum=incremental. Any other mode makes it a no-op: a full VACUUM needs
// exclusive use and stays the db:vacuum command.
func (s *Store) IncrementalVacuum(ctx context.Context) error {
	if s.dialect != dialect.SQLite {
		return nil
	}
	var mode []struct {
		Value int `db:"auto_vacuum"`
	}
	if err := s.on(ctx).Select(&mode, `PRAGMA auto_vacuum`); err != nil {
		return err
	}
	if len(mode) == 0 || mode[0].Value != 2 {
		return nil
	}
	return s.on(ctx).Statement(`PRAGMA incremental_vacuum`)
}
