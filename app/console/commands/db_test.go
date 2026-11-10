package commands

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"rllmx/app/store/dialect"
	"strings"
	"testing"

	"github.com/goravel/framework/contracts/console"
	"github.com/goravel/framework/contracts/console/command"

	"rllmx/app/store/data"
	"rllmx/app/store/lock"
	"rllmx/app/store/repository"
	"rllmx/tests/store"
)

// dbConsole answers the options and the one prompt a db command uses.
type dbConsole struct {
	fakeConsole
	opts    map[string]string
	flags   map[string]bool
	answers []string
	asked   []string
}

func (c *dbConsole) Option(k string) string   { return c.opts[k] }
func (c *dbConsole) OptionBool(k string) bool { return c.flags[k] }
func (c *dbConsole) Ask(q string, _ ...console.AskOption) (string, error) {
	c.asked = append(c.asked, q)
	if len(c.answers) == 0 {
		return "", errors.New("no answer")
	}
	a := c.answers[0]
	c.answers = c.answers[1:]
	return a, nil
}

type rig struct {
	t       *testing.T
	st      *store.Store
	dir     string
	tty     bool
	exited  int
	openErr error
	modes   []data.Mode
}

func newRig(t *testing.T) *rig {
	t.Helper()
	return &rig{t: t, st: store.Migrated(t), dir: t.TempDir(), tty: true}
}

func (r *rig) runtime() *DatabaseRuntime {
	return &DatabaseRuntime{
		Open: func(_ context.Context, m data.Mode) (*Handle, error) {
			r.modes = append(r.modes, m)
			if r.openErr != nil {
				return nil, r.openErr
			}
			return &Handle{Store: r.st.Store, DataDir: r.dir}, nil
		},
		Terminal: func() bool { return r.tty },
		Exit:     func(code int) { r.exited = code },
	}
}

func TestDBBackupWritesACopyAndPrintsOnlyItsLocation(t *testing.T) {
	r := newRig(t)
	c := &dbConsole{}
	if err := (&DBBackup{R: r.runtime()}).Handle(c); err != nil {
		t.Fatal(err)
	}
	if len(c.lines) != 1 || !strings.HasPrefix(c.lines[0], "backup written: ") {
		t.Fatalf("output %q", c.lines)
	}
	file := strings.TrimPrefix(c.lines[0], "backup written: ")
	if !strings.HasPrefix(file, filepath.Join(r.dir, "db", "backups")) {
		t.Fatalf("backup at %s, want under the data directory's backups folder", file)
	}
	if info, err := os.Stat(file); err != nil || info.Size() == 0 {
		t.Fatalf("backup file: %v %v", info, err)
	}
	if r.modes[0] != data.ModeBackup {
		t.Fatalf("mode %d: a backup opens the file read-only without query_only", r.modes[0])
	}
}

func TestDBBackupToACustomDirectory(t *testing.T) {
	r := newRig(t)
	out := t.TempDir()
	c := &dbConsole{opts: map[string]string{"out": out}}
	if err := (&DBBackup{R: r.runtime()}).Handle(c); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.output(), out) {
		t.Fatalf("output %q", c.output())
	}
	if _, err := os.Stat(filepath.Join(r.dir, "db", "backups")); err == nil {
		t.Fatal("the default folder was made although --out was given")
	}
}

func TestDBBackupIntoAMissingDirectoryFailsWithoutCreatingIt(t *testing.T) {
	r := newRig(t)
	missing := filepath.Join(t.TempDir(), "nope")
	c := &dbConsole{opts: map[string]string{"out": missing}}
	if err := (&DBBackup{R: r.runtime()}).Handle(c); err == nil {
		t.Fatal("a backup into a missing directory succeeded")
	}
	if _, err := os.Stat(missing); err == nil {
		t.Fatal("the missing directory was created")
	}
}

func TestDBBackupWithTheDefaultFolderMakesItPrivately(t *testing.T) {
	r := newRig(t)
	if err := (&DBBackup{R: r.runtime()}).Handle(&dbConsole{}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(r.dir, "db", "backups"))
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("%v %v", info, err)
	}
}

func TestDBBackupOfPostgresPrintsAHintAndExitsTwo(t *testing.T) {
	r := newRig(t)
	rt := r.runtime()
	rt.Open = func(context.Context, data.Mode) (*Handle, error) {
		return &Handle{Store: pgStore(t), DataDir: r.dir}, nil
	}
	c := &dbConsole{}
	err := (&DBBackup{R: rt}).Handle(c)
	if err == nil {
		t.Fatal("a PostgreSQL backup reported success")
	}
	if r.exited != 2 {
		t.Fatalf("exit code %d, want 2: Artisan itself only ever exits 0 or 1", r.exited)
	}
	if !strings.Contains(strings.Join(c.errs, " "), "pg_dump") {
		t.Fatalf("no pg_dump hint: %q", c.errs)
	}
}

func TestDBCheckPassesOnAHealthyDatabase(t *testing.T) {
	r := newRig(t)
	c := &dbConsole{}
	if err := (&DBCheck{R: r.runtime()}).Handle(c); err != nil {
		t.Fatal(err)
	}
	if c.output() != "database ok" {
		t.Fatalf("output %q", c.output())
	}
	if r.modes[0] != data.ModeRead {
		t.Fatalf("mode %d: a check never writes", r.modes[0])
	}
}

func TestDBCheckFailsAndNamesTheProblem(t *testing.T) {
	r := newRig(t)
	if err := r.st.SQ.DB.Statement(`ALTER TABLE "apiKeys" DROP COLUMN "accessAllow"`); err != nil {
		t.Fatal(err)
	}
	c := &dbConsole{}
	err := (&DBCheck{R: r.runtime()}).Handle(c)
	if err == nil {
		t.Fatal("a database with a missing column passed")
	}
	if !strings.Contains(c.output(), "schema: missing column apiKeys.accessAllow") {
		t.Fatalf("output %q", c.output())
	}
}

func TestDBCheckOfAMissingDatabaseFails(t *testing.T) {
	r := newRig(t)
	r.openErr = errors.New("no database at the configured path")
	if err := (&DBCheck{R: r.runtime()}).Handle(&dbConsole{}); err == nil {
		t.Fatal("check of a missing database succeeded")
	}
}

func TestDBVacuumAsksAndAcceptsYes(t *testing.T) {
	r := newRig(t)
	c := &dbConsole{answers: []string{"y"}}
	if err := (&DBVacuum{R: r.runtime()}).Handle(c); err != nil {
		t.Fatal(err)
	}
	if len(c.asked) != 1 || !strings.Contains(c.asked[0], "[y/N]") {
		t.Fatalf("prompt %q", c.asked)
	}
	if c.output() != "database vacuumed" {
		t.Fatalf("output %q", c.output())
	}
	if r.modes[0] != data.ModeWrite {
		t.Fatalf("mode %d: a vacuum needs the writer", r.modes[0])
	}
}

func TestDBVacuumDeclinedChangesNothing(t *testing.T) {
	for _, answer := range []string{"n", "", "yes please", "Y!", " "} {
		r := newRig(t)
		c := &dbConsole{answers: []string{answer}}
		err := (&DBVacuum{R: r.runtime()}).Handle(c)
		if err == nil || strings.Contains(c.output(), "vacuumed") {
			t.Errorf("answer %q was accepted", answer)
		}
		if len(r.modes) != 0 {
			t.Errorf("answer %q: the database was opened before the answer", answer)
		}
	}
}

func TestDBVacuumAcceptsYesAndYesInAnyCase(t *testing.T) {
	for _, answer := range []string{"y", "Y", "yes", "YES", " yes "} {
		r := newRig(t)
		if err := (&DBVacuum{R: r.runtime()}).Handle(&dbConsole{answers: []string{answer}}); err != nil {
			t.Errorf("answer %q refused: %v", answer, err)
		}
	}
}

func TestDBVacuumWithoutATerminalNeedsYes(t *testing.T) {
	r := newRig(t)
	r.tty = false
	c := &dbConsole{answers: []string{"y"}}
	if err := (&DBVacuum{R: r.runtime()}).Handle(c); err == nil {
		t.Fatal("vacuum ran on a non-terminal without --yes")
	}
	if len(c.asked) != 0 {
		t.Fatal("a prompt was shown without a terminal")
	}
	c = &dbConsole{flags: map[string]bool{"yes": true}}
	if err := (&DBVacuum{R: r.runtime()}).Handle(c); err != nil {
		t.Fatalf("--yes refused: %v", err)
	}
	if len(c.asked) != 0 {
		t.Fatal("--yes still asked")
	}
}

func TestDBVacuumRefusesWhileTheInstanceLockIsHeld(t *testing.T) {
	r := newRig(t)
	held, err := lock.Acquire(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	err = (&DBVacuum{R: r.runtime()}).Handle(&dbConsole{flags: map[string]bool{"yes": true}})
	if !errors.Is(err, lock.ErrHeld) {
		t.Fatalf("err = %v", err)
	}
}

func TestDBVacuumReleasesTheLockAfterwards(t *testing.T) {
	r := newRig(t)
	if err := (&DBVacuum{R: r.runtime()}).Handle(&dbConsole{flags: map[string]bool{"yes": true}}); err != nil {
		t.Fatal(err)
	}
	l, err := lock.Acquire(r.dir)
	if err != nil {
		t.Fatalf("the lock is still held after the command: %v", err)
	}
	_ = l.Release()
}

func TestDBVacuumReleasesTheLockWhenItFails(t *testing.T) {
	r := newRig(t)
	rt := r.runtime()
	inner := rt.Open
	rt.Open = func(ctx context.Context, m data.Mode) (*Handle, error) {
		if _, err := inner(ctx, m); err != nil {
			return nil, err
		}
		return nil, errors.New("open failed")
	}
	if err := (&DBVacuum{R: rt}).Handle(&dbConsole{flags: map[string]bool{"yes": true}}); err == nil {
		t.Fatal("a failed open succeeded")
	}
	l, err := lock.Acquire(r.dir)
	if err != nil {
		t.Fatalf("a failed command left the lock held: %v", err)
	}
	_ = l.Release()
}

func TestDBVacuumReclaimsSpace(t *testing.T) {
	r := newRig(t)
	big := strings.Repeat("v", 2000)
	for i := range 300 {
		if err := r.st.KV().Set(context.Background(), "bulk", strings.Repeat("k", 8)+strings.Repeat("x", i%50)+string(rune('a'+i%26))+string(rune('a'+i/26%26)), `"`+big+`"`); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.st.SQ.DB.Statement(`DELETE FROM "kv" WHERE "scope" = 'bulk'`); err != nil {
		t.Fatal(err)
	}
	if err := r.st.Checkpoint(context.Background()); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(r.st.SQ.File)
	if err := (&DBVacuum{R: r.runtime()}).Handle(&dbConsole{flags: map[string]bool{"yes": true}}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(r.st.SQ.File)
	if after.Size() >= before.Size()/2 {
		t.Fatalf("file %d -> %d bytes", before.Size(), after.Size())
	}
}

func TestDBCommandsRejectABadConfigurationWithoutQuotingIt(t *testing.T) {
	r := newRig(t)
	r.openErr = errors.New("settings: DB_ENCRYPTION_SECRET: must be exactly 64 hex characters")
	for name, cmd := range map[string]console.Command{
		"backup": &DBBackup{R: r.runtime()}, "check": &DBCheck{R: r.runtime()}, "vacuum": &DBVacuum{R: r.runtime()},
	} {
		c := &dbConsole{flags: map[string]bool{"yes": true}}
		err := cmd.Handle(c)
		if err == nil || !strings.Contains(err.Error(), "DB_ENCRYPTION_SECRET") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestDBCommandsAskNoPasswordAndTakeNoSecretFlags(t *testing.T) {
	r := newRig(t)
	for _, cmd := range []console.Command{&DBBackup{R: r.runtime()}, &DBCheck{R: r.runtime()}, &DBVacuum{R: r.runtime()}} {
		for _, f := range cmd.Extend().Flags {
			name := strings.ToLower(flagName(f))
			if strings.Contains(name, "password") || strings.Contains(name, "secret") {
				t.Errorf("%s has a secret-like flag --%s", cmd.Signature(), name)
			}
		}
	}
}

func flagName(f command.Flag) string {
	switch v := f.(type) {
	case *command.StringFlag:
		return v.Name
	case *command.BoolFlag:
		return v.Name
	}
	return ""
}

func TestTheDBCommandSignatures(t *testing.T) {
	r := newRig(t)
	for sig, cmd := range map[string]console.Command{
		"db:backup": &DBBackup{R: r.runtime()}, "db:check": &DBCheck{R: r.runtime()}, "db:vacuum": &DBVacuum{R: r.runtime()},
	} {
		if cmd.Signature() != sig || cmd.Description() == "" {
			t.Errorf("%s: signature %q description %q", sig, cmd.Signature(), cmd.Description())
		}
	}
}

var _ = dialect.SQLite

func pgStore(t *testing.T) *repository.Store {
	t.Helper()
	st := store.New(t)
	s, err := repository.New(repository.Options{DB: st.SQ.DB, Schema: st.SQ.Schema, Log: st.SQ.Log, Dialect: dialect.Postgres})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestOnlyYAndYesConfirm(t *testing.T) {
	for _, answer := range []string{"ye", "yess", "yep", "y es", "yes!", "no", "n", "ok", "1", "true"} {
		if Confirmed(&dbConsole{answers: []string{answer}}, "q [y/N]") {
			t.Errorf("answer %q was taken as a yes", answer)
		}
	}
	if Confirmed(&dbConsole{}, "q [y/N]") {
		t.Error("a prompt that failed to read was taken as a yes")
	}
}
