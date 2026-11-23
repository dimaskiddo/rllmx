package commands

import (
	"context"
	"errors"
	"strings"
	"testing"

	"rllmx/app/store/data"
	"rllmx/app/store/lock"
	"rllmx/tests/store"
)

func TestDBMigrateCreatesTheSchema(t *testing.T) {
	r := newRig(t)
	r.st = store.New(t)
	c := &dbConsole{}
	if err := (&DBMigrate{R: r.runtime()}).Handle(c); err != nil {
		t.Fatal(err)
	}
	missing, err := r.st.NeedsMigration(context.Background())
	if err != nil || missing {
		t.Fatalf("needs migration = %v, %v", missing, err)
	}
	var version []string
	if err := r.st.SQ.DB.Select(&version, `SELECT "value" FROM "_meta" WHERE "key" = 'schemaVersion'`); err != nil || len(version) != 1 {
		t.Fatalf("_meta = %v, %v", version, err)
	}
	if len(r.modes) != 1 || r.modes[0] != data.ModeInit {
		t.Fatalf("modes = %v, want ModeInit", r.modes)
	}
	if !strings.Contains(c.output(), "schema is up to date") {
		t.Fatalf("output %q", c.output())
	}
}

func TestDBMigrateRefusesWhileTheServerHoldsTheLock(t *testing.T) {
	r := newRig(t)
	r.st = store.New(t)
	held, err := lock.Acquire(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	if err := (&DBMigrate{R: r.runtime()}).Handle(&dbConsole{}); !errors.Is(err, lock.ErrHeld) {
		t.Fatalf("err = %v", err)
	}
}
