package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestWriterDSNKeepsTheSettingsTheStoreDependsOn(t *testing.T) {
	dsn := sqliteDSN(filepath.Join("/data", "with space", "db", "data.sqlite"))
	for _, want := range []string{
		"file:/data/with%20space/db/data.sqlite?",
		"_txlock=immediate",
		"_pragma=busy_timeout(5000)",
		"_pragma=journal_mode(WAL)",
		"_pragma=foreign_keys(ON)",
		"_pragma=secure_delete(ON)",
		"_pragma=synchronous(NORMAL)",
	} {
		if !strings.Contains(dsn, want) {
			t.Errorf("writer DSN %q lacks %q", dsn, want)
		}
	}
}

func TestExtraPragmasRunBeforeTheJournalMode(t *testing.T) {
	dsn := sqliteDSN("/data/analytics.sqlite", "auto_vacuum(2)")
	av, wal := strings.Index(dsn, "auto_vacuum(2)"), strings.Index(dsn, "journal_mode(WAL)")
	if av < 0 || wal < 0 || av > wal {
		t.Fatalf("auto_vacuum only applies to a file with no tables, so it must come first: %q", dsn)
	}
}

func TestReadOnlyDSNCannotWriteOrChangeTheJournal(t *testing.T) {
	dsn := sqliteReadOnlyDSN("/data/db/data.sqlite")
	for _, want := range []string{"mode=ro", "_pragma=query_only(1)"} {
		if !strings.Contains(dsn, want) {
			t.Errorf("read-only DSN %q lacks %q", dsn, want)
		}
	}
	for _, bad := range []string{"journal_mode", "_txlock", "secure_delete"} {
		if strings.Contains(dsn, bad) {
			t.Errorf("read-only DSN %q must not set %s: setting it is a write", dsn, bad)
		}
	}
}

func TestTheBackupDSNIsReadOnlyButAllowsVacuumInto(t *testing.T) {
	dsn := sqliteBackupDSN("/data/db/data.sqlite")
	if !strings.Contains(dsn, "mode=ro") {
		t.Fatalf("backup DSN %q must open the file read-only", dsn)
	}
	for _, bad := range []string{"query_only", "journal_mode", "_txlock", "secure_delete"} {
		if strings.Contains(dsn, bad) {
			t.Errorf("backup DSN %q must not set %s: query_only makes VACUUM INTO fail, the rest are writes", dsn, bad)
		}
	}
}
