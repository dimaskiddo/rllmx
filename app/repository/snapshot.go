package repository

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"rllmx/app/dialect"
	"runtime"

	contractsdb "github.com/goravel/framework/contracts/database/db"
)

// Snapshot is a read-only copy of a dialect.SQLite database made with VACUUM INTO from
// a read-only connection: one consistent view that includes un-checkpointed
// WAL commits while the owner keeps writing. The original is never written.
type Snapshot struct {
	conn *Conn
	dir  string
}

// ResolveSourceDatabase turns a path into a database file. A file is used as
// given; a directory is searched for db/data.sqlite and then data.sqlite.
func ResolveSourceDatabase(path string) (string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("repository: source %s: %w", path, err)
	}
	if !st.IsDir() {
		return path, nil
	}
	for _, rel := range []string{filepath.Join("db", dbFileName), dbFileName} {
		candidate := filepath.Join(path, rel)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("repository: no %s found in %s", dbFileName, path)
}

// OpenSnapshot copies the database at srcPath into a fresh directory under
// workDir (created 0700) and opens the copy for reading only.
func OpenSnapshot(open Opener, srcPath, workDir string) (*Snapshot, error) {
	if _, err := os.Stat(srcPath); err != nil {
		return nil, fmt.Errorf("repository: source database: %w", err)
	}
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(workDir, "src-")
	if err != nil {
		return nil, err
	}
	copyPath := filepath.Join(dir, "source.sqlite")
	if err := vacuumCopy(open, srcPath, copyPath); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	c, err := open(copyPath, dialect.BusyWait+"&_pragma=query_only(1)")
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	return &Snapshot{conn: c, dir: dir}, nil
}

// DB returns the read-only handle on the copy.
func (s *Snapshot) DB() contractsdb.DB { return s.conn.DB }

// Close closes the handle and deletes the copy. It is safe to call twice.
func (s *Snapshot) Close() error {
	if s.conn == nil {
		return nil
	}
	err := s.conn.Close()
	s.conn = nil
	return errors.Join(err, os.RemoveAll(s.dir))
}

func vacuumCopy(open Opener, srcPath, dst string) error {
	abs, err := filepath.Abs(srcPath)
	if err != nil {
		return err
	}
	src, err := open(abs, "mode=ro&"+dialect.BusyWait)
	if err != nil {
		return err
	}
	defer src.Close()
	if err := src.DB.Statement(`VACUUM INTO ?`, dst); err != nil {
		return fmt.Errorf("repository: copy source database: %w", err)
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	return os.Chmod(dst, 0o600)
}
