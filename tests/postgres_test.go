package tests

import (
	"os"
	"testing"
)

// setDSN points the suite at a server for the length of one test.
func setDSN(t *testing.T, dsn string) {
	t.Helper()
	t.Setenv(PGDSN, dsn)
}

func TestUsingPostgresFollowsTheVariable(t *testing.T) {
	setDSN(t, "")
	if UsingPostgres() {
		t.Fatal("PostgreSQL is selected with no DSN")
	}
	setDSN(t, "postgres://example")
	if !UsingPostgres() {
		t.Fatal("SQLite is selected although a DSN is set")
	}
}

func TestOpenAnyUsesSQLiteWhenNoServerIsSet(t *testing.T) {
	setDSN(t, "")
	c := OpenAny(t)
	if c == nil || c.File == "" || c.PGSchema != "" {
		t.Fatalf("OpenAny did not open a SQLite file: %+v", c)
	}
	if _, err := os.Stat(c.File); err != nil {
		t.Fatalf("the file is not there: %v", err)
	}
}
