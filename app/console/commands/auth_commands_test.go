package commands

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/goravel/framework/contracts/console"

	frameworkhash "github.com/goravel/framework/contracts/hash"

	"rllmx/app/access/auth"
	"rllmx/app/access/identity"
	"rllmx/app/store/data"
	"rllmx/app/store/repository"
	"rllmx/tests"
	"rllmx/tests/store"
)

// authConsole answers the hidden prompts and the one yes/no question the auth
// commands use. A prompt that was never queued is an error, so a command that
// asks for more than the test expects fails instead of hanging.
type authConsole struct {
	fakeConsole
	opts    map[string]string
	flags   map[string]bool
	secrets []string
	asked   []string
	errors  []string
	table   [][]string
	headers []string
}

func (c *authConsole) Option(k string) string   { return c.opts[k] }
func (c *authConsole) OptionBool(k string) bool { return c.flags[k] }
func (c *authConsole) Argument(int) string      { return c.opts["arg0"] }

func (c *authConsole) Ask(q string, _ ...console.AskOption) (string, error) {
	c.asked = append(c.asked, q)
	if len(c.secrets) == 0 {
		return "", errors.New("no answer")
	}
	a := c.secrets[0]
	c.secrets = c.secrets[1:]
	return a, nil
}

func (c *authConsole) Secret(q string, _ ...console.SecretOption) (string, error) {
	c.asked = append(c.asked, q)
	if len(c.secrets) == 0 {
		return "", errors.New("no secret queued")
	}
	a := c.secrets[0]
	c.secrets = c.secrets[1:]
	return a, nil
}

func (c *authConsole) Warning(m string) { c.errors = append(c.errors, m) }

func (c *authConsole) Table(headers []string, rows [][]string, _ ...console.TableOption) {
	c.headers, c.table = headers, rows
}

// authRig is one command run over a real store and a temp data directory.
type authRig struct {
	t   *testing.T
	st  *store.Store
	dir string
	tty bool
	now time.Time
}

func newAuthRig(t *testing.T) *authRig {
	t.Helper()
	return &authRig{t: t, st: store.Migrated(t), dir: t.TempDir(), tty: true, now: time.Now()}
}

func (r *authRig) runtime() *AuthRuntime {
	db := &DatabaseRuntime{
		Open: func(_ context.Context, _ data.Mode) (*Handle, error) {
			return &Handle{Store: r.st.Store, DataDir: r.dir}, nil
		},
		Terminal: func() bool { return r.tty },
		Exit:     func(int) {},
		Facades: func() Facades {
			return Facades{Config: tests.Config{Values: map[string]any{"app.data_dir": r.dir}}}
		},
	}
	a := NewAuthRuntime(db, func() frameworkhash.Hash { return routeHash{} })
	a.Now = func() time.Time { return r.now }
	a.Sleep = func(time.Duration) {}
	return a
}

type routeHash struct{}

func (routeHash) Make(v string) (string, error) { return "hashed:" + v, nil }
func (routeHash) Check(v, h string) bool        { return h == "hashed:"+v }
func (routeHash) NeedsRehash(string) bool       { return false }

func (r *authRig) storePassword(t *testing.T, password string) {
	t.Helper()
	if err := r.st.Settings().SetPasswordHash(context.Background(), "hashed:"+password); err != nil {
		t.Fatal(err)
	}
}

func TestKeysCreatePrintsTheKeyOnceAndStoresItActive(t *testing.T) {
	r := newAuthRig(t)
	r.storePassword(t, "a-known-password")
	machine := identity.NewMachine("")
	if _, err := machine.Ensure(r.dir); err != nil {
		t.Fatal(err)
	}

	c := &authConsole{opts: map[string]string{"name": "ci"}, secrets: []string{"a-known-password"}}
	if err := (&KeysCreate{R: r.runtime()}).Handle(c); err != nil {
		t.Fatal(err)
	}
	var printed string
	for _, line := range c.lines {
		if strings.HasPrefix(line, "sk-") {
			printed = line
		}
	}
	if printed == "" {
		t.Fatalf("no key printed: %q", c.lines)
	}
	if identity.NewKeys("").Classify(printed) != identity.ClassOK {
		t.Fatalf("the printed key does not classify as ok: %q", printed)
	}

	rows, err := r.st.APIKeys().List(context.Background())
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows %d %v", len(rows), err)
	}
	if rows[0].Key != printed || !rows[0].IsActive || rows[0].Name != "ci" {
		t.Fatalf("%+v", rows[0])
	}
}

func TestKeysCreateRefusesWithoutAMachineID(t *testing.T) {
	r := newAuthRig(t)
	r.storePassword(t, "a-known-password")
	c := &authConsole{opts: map[string]string{"name": "ci"}, secrets: []string{"a-known-password"}}
	err := (&KeysCreate{R: r.runtime()}).Handle(c)
	if err == nil || !strings.Contains(err.Error(), "machine id") {
		t.Fatalf("err %v", err)
	}
}

func TestKeysCreateRefusesABadName(t *testing.T) {
	r := newAuthRig(t)
	for _, name := range []string{"", " ", strings.Repeat("x", 65), "a\nb"} {
		c := &authConsole{opts: map[string]string{"name": name}, secrets: []string{"a-known-password"}}
		if err := (&KeysCreate{R: r.runtime()}).Handle(c); err == nil {
			t.Errorf("%q accepted", name)
		}
	}
}

func TestKeysCreateRefusesWithoutATerminal(t *testing.T) {
	r := newAuthRig(t)
	r.tty = false
	c := &authConsole{opts: map[string]string{"name": "ci"}, secrets: []string{"a-known-password"}}
	err := (&KeysCreate{R: r.runtime()}).Handle(c)
	if err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Fatalf("err %v", err)
	}
}

func TestKeysCreateRefusesTheWrongPasswordThreeTimes(t *testing.T) {
	r := newAuthRig(t)
	r.storePassword(t, "a-known-password")
	c := &authConsole{opts: map[string]string{"name": "ci"}, secrets: []string{"wrong", "wrong", "wrong"}}
	err := (&KeysCreate{R: r.runtime()}).Handle(c)
	if err == nil || !strings.Contains(err.Error(), "wrong password") {
		t.Fatalf("err %v", err)
	}
	if rows, _ := r.st.APIKeys().List(context.Background()); len(rows) != 0 {
		t.Fatal("a key was created after a failed verification")
	}
}

func TestKeysCreateNeverAcceptsAnEmptyAnswer(t *testing.T) {
	r := newAuthRig(t)
	c := &authConsole{opts: map[string]string{"name": "ci"}, secrets: []string{""}}
	err := (&KeysCreate{R: r.runtime()}).Handle(c)
	if err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Fatalf("err %v", err)
	}
}

func TestKeysListMasksTheKeyAndShowsTheClass(t *testing.T) {
	r := newAuthRig(t)
	r.storePassword(t, "a-known-password")
	key, err := identity.NewKeys("").New("0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.APIKeys().Create(context.Background(), "ci", "0123456789abcdef", key); err != nil {
		t.Fatal(err)
	}

	c := &authConsole{secrets: []string{"a-known-password"}}
	if err := (&KeysList{R: r.runtime()}).Handle(c); err != nil {
		t.Fatal(err)
	}
	if len(c.table) != 1 {
		t.Fatalf("rows %v", c.table)
	}
	for _, cell := range c.table[0] {
		if cell == key {
			t.Fatal("the full key was printed")
		}
	}
	if c.table[0][3] != "ok" || c.table[0][4] != "active" {
		t.Fatalf("row %v", c.table[0])
	}
}

func TestKeysListReportsAMismatchedChecksumAndARevokedKey(t *testing.T) {
	r := newAuthRig(t)
	r.storePassword(t, "a-known-password")
	ctx := context.Background()
	key, _ := identity.NewKeys("").New("0123456789abcdef")
	row, err := r.st.APIKeys().Create(ctx, "ci", "0123456789abcdef", key)
	if err != nil {
		t.Fatal(err)
	}
	off := false
	if _, err := r.st.APIKeys().Update(ctx, row.ID, repository.APIKeyPatch{IsActive: &off}); err != nil {
		t.Fatal(err)
	}
	legacy, _ := r.st.APIKeys().Create(ctx, "old", "", "sk-legacykey1")

	c := &authConsole{secrets: []string{"a-known-password"}}
	if err := (&KeysList{R: r.runtime()}).Handle(c); err != nil {
		t.Fatal(err)
	}
	if len(c.table) != 2 {
		t.Fatalf("rows %v", c.table)
	}
	// Two keys made in one millisecond have no stored order, so the rows are
	// found by id rather than by position.
	byID := map[string][]string{}
	for _, r := range c.table {
		byID[r[0]] = r
	}
	if r := byID[row.ID]; len(r) < 5 || r[4] != "revoked" || r[3] != "ok" {
		t.Errorf("revoked key row %v", r)
	}
	if r := byID[legacy.ID]; len(r) < 5 || r[3] != "legacy" || r[4] != "active" {
		t.Errorf("legacy key row %v", r)
	}
}

func TestKeysRevokeDeactivates(t *testing.T) {
	r := newAuthRig(t)
	r.storePassword(t, "a-known-password")
	key, _ := identity.NewKeys("").New("0123456789abcdef")
	row, err := r.st.APIKeys().Create(context.Background(), "ci", "0123456789abcdef", key)
	if err != nil {
		t.Fatal(err)
	}

	c := &authConsole{opts: map[string]string{"arg0": row.ID}, secrets: []string{"a-known-password"}}
	if err := (&KeysRevoke{R: r.runtime()}).Handle(c); err != nil {
		t.Fatal(err)
	}
	got, ok, err := r.st.APIKeys().GetByID(context.Background(), row.ID)
	if err != nil || !ok || got.IsActive {
		t.Fatalf("the key is still active: %+v (%v)", got, err)
	}
}

func TestKeysRevokeNeedsAnID(t *testing.T) {
	r := newAuthRig(t)
	r.storePassword(t, "a-known-password")
	c := &authConsole{opts: map[string]string{}, secrets: []string{"a-known-password"}}
	if err := (&KeysRevoke{R: r.runtime()}).Handle(c); err == nil {
		t.Fatal("a revoke with no id was accepted")
	}
}

func TestPasswordResetStoresAHashAndBumpsTheEpoch(t *testing.T) {
	r := newAuthRig(t)
	r.storePassword(t, "a-known-password")
	ctx := context.Background()
	before, _ := r.st.Settings().Effective(ctx)
	beforeHash, _ := before.GetString("password")

	c := &authConsole{secrets: []string{"a-known-password", "a-much-better-passphrase", "a-much-better-passphrase"}}
	if err := (&PasswordReset{R: r.runtime()}).Handle(c); err != nil {
		t.Fatal(err)
	}
	after, _ := r.st.Settings().Effective(ctx)
	afterHash, _ := after.GetString("password")
	if afterHash == beforeHash || afterHash != "hashed:a-much-better-passphrase" {
		t.Fatalf("hash %q", afterHash)
	}
	epoch, found, err := r.st.KV().Get(ctx, auth.ScopeSession, auth.EpochKey)
	if err != nil || !found || epoch == "0" {
		t.Fatalf("epoch %q found=%v err=%v", epoch, found, err)
	}
}

func TestPasswordResetJudgesTheNewPassword(t *testing.T) {
	r := newAuthRig(t)
	r.storePassword(t, "a-known-password")
	for _, bad := range []string{"123456", "short"} {
		c := &authConsole{secrets: []string{"a-known-password", bad, bad}}
		if err := (&PasswordReset{R: r.runtime()}).Handle(c); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestPasswordResetRefusesATypo(t *testing.T) {
	r := newAuthRig(t)
	r.storePassword(t, "a-known-password")
	c := &authConsole{secrets: []string{"a-known-password", "a-much-better-passphrase", "a-much-better-passphras"}}
	if err := (&PasswordReset{R: r.runtime()}).Handle(c); err == nil {
		t.Fatal("two different passwords were accepted")
	}
}

func TestDBExportPlainNeedsThePasswordBeforeItWritesAnything(t *testing.T) {
	r := newAuthRig(t)
	r.storePassword(t, "a-known-password")
	dir := t.TempDir()
	c := &authConsole{opts: map[string]string{"out": dir}, flags: map[string]bool{"yes": true}, secrets: []string{"wrong", "wrong", "wrong"}}
	if err := (&DBExportPlain{R: r.runtime()}).Handle(c); err == nil {
		t.Fatal("a wrong password produced a copy")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("the failed run wrote %d entries", len(entries))
	}
}

func TestDBExportPlainRefusesToStartWithoutTheOpener(t *testing.T) {
	r := newAuthRig(t)
	r.storePassword(t, "a-known-password")
	c := &authConsole{opts: map[string]string{"out": t.TempDir()}, flags: map[string]bool{"yes": true}, secrets: []string{"a-known-password"}}
	if err := (&DBExportPlain{R: r.runtime()}).Handle(c); err == nil {
		t.Fatal("an export with no way to open the copy was accepted")
	}
}
