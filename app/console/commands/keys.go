package commands

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/goravel/framework/contracts/console"
	"github.com/goravel/framework/contracts/console/command"

	"rllmx/app/access/auth"
	"rllmx/app/access/identity"
	"rllmx/app/store/data"
	"rllmx/app/store/repository"
)

// errNoTerminal and errWrongPassword are the two refusals the auth commands
// share. Neither repeats a value.
var (
	errNoTerminal = func(noun string) error {
		return errors.New(noun + " needs a terminal to ask for the dashboard password")
	}
	errWrongPassword = errors.New("wrong password")
)

// KeysCreate mints one client key bound to this machine.
type KeysCreate struct{ R *AuthRuntime }

func (c *KeysCreate) Signature() string { return "keys:create" }

func (c *KeysCreate) Description() string { return "Create a client API key and print it once" }

func (c *KeysCreate) Extend() command.Extend {
	return command.Extend{Category: "keys", Flags: []command.Flag{
		&command.StringFlag{Name: "name", Usage: "a name for the key, at most 64 characters"},
	}}
}

func (c *KeysCreate) Handle(ctx console.Context) error {
	name := strings.TrimSpace(ctx.Option("name"))
	if name == "" {
		answer, err := ctx.Ask("Key name")
		if err != nil {
			return err
		}
		name = strings.TrimSpace(answer)
	}
	if !auth.ValidName(name) {
		return errors.New("keys:create: the name must be 1 to 64 bytes with no control character")
	}

	h, err := c.R.Open(ctx)
	if err != nil {
		return err
	}
	ok, err := c.R.Verify(ctx, h, "keys:create")
	if err != nil || !ok {
		return err
	}

	cfg := c.R.R.Facades().Config
	machine := identity.NewMachine(cfg.GetString("auth.machine_id_salt"))
	machineID, found := machine.Load(c.R.R.Facades().Config.GetString("app.data_dir"))
	if !found {
		return errors.New("keys:create: no machine id; start the server once so it can create one")
	}
	key, err := identity.NewKeys(cfg.GetString("auth.api_key_secret")).New(machineID)
	if err != nil {
		return err
	}

	row, err := h.Store.APIKeys().Create(ctx, name, machineID, key)
	if err != nil {
		return err
	}
	ctx.Line("key created: " + row.Name)
	ctx.Line(row.Key)
	ctx.Line("id: " + row.ID)
	ctx.Warning("This key is printed once; store it now.")
	return nil
}

// KeysList prints every key with a masked value and its checksum class.
type KeysList struct{ R *AuthRuntime }

func (c *KeysList) Signature() string { return "keys:list" }

func (c *KeysList) Description() string { return "List client API keys with their checksum class" }

func (c *KeysList) Extend() command.Extend { return command.Extend{Category: "keys"} }

func (c *KeysList) Handle(ctx console.Context) error {
	h, err := c.R.Open(ctx)
	if err != nil {
		return err
	}
	ok, err := c.R.Verify(ctx, h, "keys:list")
	if err != nil || !ok {
		return err
	}
	rows, err := h.Store.APIKeys().List(ctx)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		ctx.Line("no keys")
		return nil
	}
	codec := identity.NewKeys(c.R.R.Facades().Config.GetString("auth.api_key_secret"))
	table := make([][]string, 0, len(rows))
	for _, row := range rows {
		state := "active"
		if !row.IsActive {
			state = "revoked"
		}
		class := "unreadable"
		if !row.Unreadable {
			class = codec.Classify(row.Key).String()
		}
		table = append(table, []string{row.ID, row.Name, maskKey(row.Key), class, state, row.CreatedAt})
	}
	ctx.Table([]string{"id", "name", "key", "checksum", "state", "created"}, table)
	return nil
}

// KeysRevoke deactivates one key.
type KeysRevoke struct{ R *AuthRuntime }

func (c *KeysRevoke) Signature() string { return "keys:revoke {id}" }

func (c *KeysRevoke) Description() string { return "Deactivate a client API key" }

func (c *KeysRevoke) Extend() command.Extend { return command.Extend{Category: "keys"} }

func (c *KeysRevoke) Handle(ctx console.Context) error {
	id := strings.TrimSpace(ctx.Argument(0))
	if id == "" {
		return errors.New("keys:revoke: an id is required")
	}
	h, err := c.R.Open(ctx)
	if err != nil {
		return err
	}
	ok, err := c.R.Verify(ctx, h, "keys:revoke")
	if err != nil || !ok {
		return err
	}
	off := false
	row, err := h.Store.APIKeys().Update(ctx, id, repository.APIKeyPatch{IsActive: &off})
	if err != nil {
		return err
	}
	ctx.Line("key revoked: " + row.Name + " (" + row.ID + ")")
	return nil
}

// maskKey shows the class and the last four characters, never the whole value.
func maskKey(key string) string {
	if key == "" {
		return "(unreadable)"
	}
	class := ""
	if i := strings.LastIndex(key, "-"); i >= 0 && i+1 < len(key) {
		class = "…" + key[i+1:]
	}
	if len(key) <= 12 {
		return "…" + key[len(key)/2:] + class
	}
	return key[:5] + "…" + key[len(key)-4:]
}

// PasswordReset replaces the dashboard password and ends every session.
type PasswordReset struct{ R *AuthRuntime }

func (c *PasswordReset) Signature() string { return "password:reset" }

func (c *PasswordReset) Description() string {
	return "Change the dashboard password and end every session"
}

func (c *PasswordReset) Extend() command.Extend { return command.Extend{Category: "password"} }

func (c *PasswordReset) Handle(ctx console.Context) error {
	h, err := c.R.Open(ctx)
	if err != nil {
		return err
	}
	ok, err := c.R.Verify(ctx, h, "password:reset")
	if err != nil || !ok {
		return err
	}

	next, err := ctx.Secret("New password")
	if err != nil {
		return err
	}
	if err := auth.Judge(next); err != nil {
		return err
	}
	again, err := ctx.Secret("Repeat the new password")
	if err != nil {
		return err
	}
	if again != next {
		return errors.New("password:reset: the two passwords differ")
	}

	hash, err := c.R.passwords().Hash(next)
	if err != nil {
		return err
	}
	if err := h.Store.Settings().SetPasswordHash(ctx, hash); err != nil {
		return err
	}
	epoch, err := h.Store.KV().Increment(ctx, auth.ScopeSession, auth.EpochKey)
	if err != nil {
		return fmt.Errorf("password:reset: the password was stored, but the session epoch did not move: %w", err)
	}
	ctx.Line("password changed; every session ended (epoch " + fmt.Sprint(epoch) + ")")
	return nil
}

// DBExportPlain writes a copy of the database with every credential in plain
// text, for going back to 9router.
type DBExportPlain struct{ R *AuthRuntime }

func (c *DBExportPlain) Signature() string { return "db:export-plain" }

func (c *DBExportPlain) Description() string {
	return "Write a database copy with every credential in plain text"
}

func (c *DBExportPlain) Extend() command.Extend {
	return command.Extend{Category: "db", Flags: []command.Flag{
		&command.StringFlag{Name: "out", Usage: "existing directory for the copy (default: the data directory's backups folder)"},
		&command.BoolFlag{Name: "yes", Usage: "skip the confirmation prompt"},
	}}
}

func (c *DBExportPlain) Handle(ctx console.Context) error {
	h, err := c.R.Open(ctx)
	if err != nil {
		return err
	}
	ok, err := c.R.Verify(ctx, h, "db:export-plain")
	if err != nil || !ok {
		return err
	}
	dir := ctx.Option("out")
	if dir == "" {
		dir = data.BackupDir(h.DataDir)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("db: create %s: %w", filepath.Base(dir), err)
		}
	}
	if err := c.R.R.confirm(ctx, "db:export-plain", "Write a copy with every credential in plain text?"); err != nil {
		return err
	}
	file, err := h.Store.ExportPlain(ctx, dir)
	if errors.Is(err, repository.ErrExportUnsupported) {
		ctx.Error(err.Error())
		c.R.R.exit(2)
		return err
	}
	if err != nil {
		return err
	}
	ctx.Line("plain copy written: " + file)
	ctx.Warning("This copy holds every credential in clear text; keep it private.")
	return nil
}
