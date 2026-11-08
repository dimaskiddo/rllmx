package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.Mode().Perm()
}

func TestPrepareMakesTheDataDirectoryAndAnEmptyPrivateDatabase(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	db := filepath.Join(data, "db", "data.sqlite")
	if err := prepare(data, db); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if m := mode(t, filepath.Join(data, "db")); m != 0o700 {
			t.Errorf("db directory mode %o", m)
		}
		if m := mode(t, db); m != 0o600 {
			t.Errorf("database file mode %o", m)
		}
	}
	if st, _ := os.Stat(db); st.Size() != 0 {
		t.Errorf("the file has %d bytes: it must be empty, the store creates the tables", st.Size())
	}
}

func TestPrepareLeavesAnExistingDatabaseAlone(t *testing.T) {
	data := t.TempDir()
	db := filepath.Join(data, "db", "data.sqlite")
	if err := os.MkdirAll(filepath.Dir(db), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(db, []byte("SQLite format 3\x00 existing rows"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := prepare(data, db); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(db)
	if string(got) != "SQLite format 3\x00 existing rows" {
		t.Fatalf("an existing database was changed: %q", got)
	}
	if runtime.GOOS != "windows" && mode(t, db) != 0o644 {
		t.Errorf("mode of an existing file was changed to %o: it belongs to its owner", mode(t, db))
	}
}

func TestPrepareNeverTouchesAFileOutsideTheDataDirectory(t *testing.T) {
	data := t.TempDir()
	elsewhere := filepath.Join(t.TempDir(), "9router", "db", "data.sqlite")
	if err := prepare(data, elsewhere); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(elsewhere)); !os.IsNotExist(err) {
		t.Fatal("a folder outside DATA_DIR was made")
	}
	if _, err := os.Stat(elsewhere); !os.IsNotExist(err) {
		t.Fatal("a file outside DATA_DIR was made")
	}
	if _, err := os.Stat(filepath.Join(data, "db")); err != nil {
		t.Fatal("the data directory's own db folder is still prepared")
	}
}

func TestPrepareTreatsADotDotPathAsOutside(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	sneaky := filepath.Join(data, "..", "other", "data.sqlite")
	if err := prepare(data, sneaky); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(data), "other")); !os.IsNotExist(err) {
		t.Fatal("a path that climbs out of DATA_DIR was treated as inside")
	}
}

func TestPrepareRefusesADataDirectoryThatIsAFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err := prepare(f, filepath.Join(f, "db", "data.sqlite"))
	if err == nil || !strings.Contains(err.Error(), "DATA_DIR") {
		t.Fatalf("error = %v", err)
	}
}

func TestPrepareWithAnEmptyDatabasePathOnlyMakesTheDirectories(t *testing.T) {
	data := t.TempDir()
	if err := prepare(data, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(data, "db")); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Join(data, "db"))
	if len(entries) != 0 {
		t.Fatalf("files were made: %v", entries)
	}
}

func TestPrepareTwiceIsHarmless(t *testing.T) {
	data := t.TempDir()
	db := filepath.Join(data, "db", "data.sqlite")
	for range 2 {
		if err := prepare(data, db); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTheRealConfigPointsTheWriterInsideTheDataDirectory(t *testing.T) {
	dir := dataDir()
	got := databaseFile("sqlite", dir)
	if !strings.HasPrefix(got, dir) {
		t.Fatalf("the default database %q is outside DATA_DIR %q", got, dir)
	}
}

func TestOnlyASQLiteConnectionHasAFileToPrepare(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []string{"postgres", "mysql", ""} {
		if got := databaseFile(c, dir); got != "" {
			t.Errorf("connection %q has the file %q", c, got)
		}
	}
	if got := databaseFile("sqlite", dir); got == "" {
		t.Error("the SQLite connection has no file")
	}
}

func TestInside(t *testing.T) {
	base := filepath.Join(string(filepath.Separator), "var", "lib", "rllmx")
	cases := map[string]bool{
		filepath.Join(base, "db", "data.sqlite"):                   true,
		filepath.Join(base, "x"):                                   true,
		base:                                                       true,
		filepath.Join(base, "..", "other"):                         false,
		filepath.Join(base, "..", "rllmx-2", "x"):                  false,
		filepath.Join(base+"-copy", "db"):                          false,
		filepath.Join(string(filepath.Separator), "etc", "passwd"): false,
		"relative/path":                                            false,
	}
	for p, want := range cases {
		if got := inside(base, p); got != want {
			t.Errorf("inside(%q, %q) = %v, want %v", base, p, got, want)
		}
	}
}
