package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// databaseFile is the path of the writable configuration database when the
// connection is SQLite, or "" for any other engine. DB_DATABASE names it;
// empty means DATA_DIR/db/data.sqlite.
func databaseFile(connection, dir string) string {
	if connection != "sqlite" {
		return ""
	}
	return envPath("DB_DATABASE", filepath.Join(dir, "db", "data.sqlite"))
}

// prepare makes DATA_DIR/db (0700) and, when the database file is inside
// DATA_DIR and missing, an empty one (0600). Goravel boots the DB facade for
// every command and pings the default connection read-write: without the
// folder that prints a warning, and with it the driver would create the file
// itself with the umask's mode. An existing file is left exactly as it is, and
// a path outside DATA_DIR, such as a 9router database, is never created and its
// folder never made.
func prepare(dataDir, dbFile string) error {
	if err := os.MkdirAll(filepath.Join(dataDir, "db"), 0o700); err != nil {
		return errors.New("settings: DATA_DIR: cannot create the db folder")
	}
	if dbFile == "" || !inside(dataDir, dbFile) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dbFile), 0o700); err != nil {
		return errors.New("settings: DATA_DIR: cannot create the database folder")
	}
	f, err := os.OpenFile(dbFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	switch {
	case err == nil:
		return f.Close()
	case errors.Is(err, os.ErrExist):
		return nil
	}
	return fmt.Errorf("settings: DATA_DIR: cannot create %s", filepath.Base(dbFile))
}

// inside reports whether path is below dir, after both are cleaned, so a
// climb with ".." counts as outside.
func inside(dir, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(path))
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
