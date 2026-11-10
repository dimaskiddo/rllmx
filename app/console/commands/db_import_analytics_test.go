package commands

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rllmx/app/store/lock"
	"rllmx/app/store/repository"
	"rllmx/tests"
)

func importedRows(t *testing.T, r *importRig) int64 {
	t.Helper()
	if r.usage == nil {
		return 0
	}
	sq := tests.OpenSQLiteReadOnly(t, r.usage.Path())
	defer sq.Close()
	var n []int64
	if err := sq.DB.Select(&n, `SELECT count(*) FROM "requests"`); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n[0]
}

func hasTable(t *testing.T, r *importRig, file, name string) bool {
	t.Helper()
	sq := tests.OpenSQLiteReadOnly(t, file)
	defer sq.Close()
	ok, err := repository.TableExists(context.Background(), sq.DB, name)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func TestImportAnalyticsReadsAFile(t *testing.T) {
	r := newImportRig(t)
	src := legacySource(t)
	c := &importConsole{dbConsole{opts: map[string]string{"from": src}}}
	if err := (&DBImportAnalytics{R: r.runtime()}).Handle(c); err != nil {
		t.Fatal(err)
	}
	out := c.output()
	if !strings.Contains(out, "read 2 rows: 2 new, 0 already imported, 0 unreadable") {
		t.Fatalf("output %q", out)
	}
	if !strings.Contains(out, "verified: 2 rows") {
		t.Fatalf("output %q", out)
	}
	if importedRows(t, r) != 2 {
		t.Fatalf("%d rows imported", importedRows(t, r))
	}
}

func TestImportAnalyticsSecondRunAddsNothing(t *testing.T) {
	r := newImportRig(t)
	src := legacySource(t)
	first := &importConsole{dbConsole{opts: map[string]string{"from": src}}}
	if err := (&DBImportAnalytics{R: r.runtime()}).Handle(first); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first.output(), "2 new") {
		t.Fatalf("first run: %q", first.output())
	}
	second := &importConsole{dbConsole{opts: map[string]string{"from": src}}}
	if err := (&DBImportAnalytics{R: r.runtime()}).Handle(second); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(second.output(), "0 new, 2 already imported") {
		t.Fatalf("second run: %q", second.output())
	}
	if importedRows(t, r) != 2 {
		t.Fatalf("%d rows after two runs", importedRows(t, r))
	}
}

func TestImportAnalyticsDefaultsToTheMainDatabase(t *testing.T) {
	r := newImportRig(t)
	// The main database holds no usage history, so the run reports an
	// unreadable source rather than importing nothing silently.
	c := &importConsole{dbConsole{}}
	if err := (&DBImportAnalytics{R: r.runtime()}).Handle(c); err == nil {
		t.Fatal("a source without usageHistory was accepted")
	}
}

func TestImportAnalyticsMissingSourceFails(t *testing.T) {
	r := newImportRig(t)
	c := &importConsole{dbConsole{opts: map[string]string{"from": filepath.Join(t.TempDir(), "nope.sqlite")}}}
	if err := (&DBImportAnalytics{R: r.runtime()}).Handle(c); err == nil {
		t.Fatal("a missing source was accepted")
	}
}

func TestImportAnalyticsRefusesWhileTheLockIsHeld(t *testing.T) {
	r := newImportRig(t)
	held, err := lock.Acquire(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	c := &importConsole{dbConsole{opts: map[string]string{"from": legacySource(t)}}}
	if err := (&DBImportAnalytics{R: r.runtime()}).Handle(c); !errors.Is(err, lock.ErrHeld) {
		t.Fatalf("err = %v", err)
	}
}

func TestDropOldNeedsATerminalAndTheTypedWord(t *testing.T) {
	for name, answer := range map[string]string{
		"lowercase": "drop", "empty": "", "yes": "yes", "padded": " DROP ",
	} {
		t.Run(name, func(t *testing.T) {
			r := newImportRig(t)
			src := legacySource(t)
			wantDrop := answer == " DROP "
			c := &importConsole{dbConsole{opts: map[string]string{"from": src}, flags: map[string]bool{"drop-old": true}, answers: []string{answer}}}
			err := (&DBImportAnalytics{R: r.runtime()}).Handle(c)
			gone := !hasTable(t, r, src, "usageHistory")
			if wantDrop {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				if !gone {
					t.Fatal("the tables are still there")
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "cancelled") {
				t.Fatalf("err = %v", err)
			}
			if gone {
				t.Fatal("the tables were dropped without the word")
			}
		})
	}
}

func TestDropOldWithoutATerminalIsRefused(t *testing.T) {
	r := newImportRig(t)
	r.tty = false
	src := legacySource(t)
	c := &importConsole{dbConsole{opts: map[string]string{"from": src}, flags: map[string]bool{"drop-old": true}, answers: []string{"DROP"}}}
	if err := (&DBImportAnalytics{R: r.runtime()}).Handle(c); err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Fatalf("err = %v", err)
	}
	if !hasTable(t, r, src, "usageHistory") {
		t.Fatal("the tables were dropped without a terminal")
	}
}

func TestDropOldRemovesTheThreeTablesAfterABackup(t *testing.T) {
	r := newImportRig(t)
	src := legacySource(t)
	c := &importConsole{dbConsole{opts: map[string]string{"from": src}, flags: map[string]bool{"drop-old": true}, answers: []string{"DROP"}}}
	if err := (&DBImportAnalytics{R: r.runtime()}).Handle(c); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.output(), "backup written: "+filepath.Join(r.dir, "db", "backups")) {
		t.Fatalf("no backup line: %q", c.output())
	}
	for _, name := range []string{"usageHistory", "usageDaily", "requestDetails"} {
		if hasTable(t, r, src, name) {
			t.Errorf("%s is still there", name)
		}
	}
	if !hasTable(t, r, src, "proxyPoolFitness") {
		t.Fatal("the fork's table was dropped")
	}
	entries, err := os.ReadDir(filepath.Join(r.dir, "db", "backups"))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "before-drop-") {
			found = true
			info, err := os.Stat(filepath.Join(r.dir, "db", "backups", e.Name(), "data.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Errorf("backup file mode %o", info.Mode().Perm())
			}
		}
	}
	if !found {
		t.Fatalf("no before-drop backup: %v", entries)
	}
}

func TestDropOldNeverRunsWhenVerificationFails(t *testing.T) {
	r := newImportRig(t)
	src := legacySource(t)
	sq := tests.OpenSQLite(t, src)
	if err := sq.DB.Statement(`INSERT INTO "usageHistory"("timestamp","provider","promptTokens") VALUES('not a time','openai',1)`); err != nil {
		t.Fatal(err)
	}
	sq.Close()
	c := &importConsole{dbConsole{opts: map[string]string{"from": src}, flags: map[string]bool{"drop-old": true}, answers: []string{"DROP"}}}
	if err := (&DBImportAnalytics{R: r.runtime()}).Handle(c); err == nil || !strings.Contains(err.Error(), "could not be imported") {
		t.Fatalf("err = %v", err)
	}
	if len(c.asked) != 0 {
		t.Fatalf("the word was asked for anyway: %v", c.asked)
	}
	if !hasTable(t, r, src, "usageHistory") {
		t.Fatal("the tables were dropped after an incomplete import")
	}
}

func TestImportAnalyticsDeclaresItsFlags(t *testing.T) {
	got := map[string]bool{}
	for _, f := range (&DBImportAnalytics{}).Extend().Flags {
		got[flagName(f)] = true
	}
	for _, want := range []string{"from", "drop-old"} {
		if !got[want] {
			t.Errorf("%s is not declared: %v", want, got)
		}
	}
	if (&DBImportAnalytics{}).Signature() != "db:import-analytics" {
		t.Fatalf("signature %q", (&DBImportAnalytics{}).Signature())
	}
}
