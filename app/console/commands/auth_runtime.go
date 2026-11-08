package commands

import (
	"context"
	"time"

	"github.com/goravel/framework/contracts/console"
	frameworkhash "github.com/goravel/framework/contracts/hash"

	"rllmx/app/auth"
	"rllmx/app/data"
)

// PasswordAttempts is how many tries a command gives for the dashboard
// password before it refuses the run.
const PasswordAttempts = 3

// PasswordPause is how long a command waits after a wrong password, so a wrong
// answer costs more than a correct one.
const PasswordPause = 2 * time.Second

// AuthRuntime is what the commands that prove the dashboard password need.
// Open reads the store the way the server does; Hash is the framework's bcrypt
// hasher; Now and Sleep let a test run without waiting.
type AuthRuntime struct {
	Open  func(ctx context.Context) (*Handle, error)
	Hash  func() frameworkhash.Hash
	Now   func() time.Time
	Sleep func(time.Duration)
	R     *DatabaseRuntime
}

// NewAuthRuntime builds the runtime the auth commands share.
func NewAuthRuntime(r *DatabaseRuntime, hasher func() frameworkhash.Hash) *AuthRuntime {
	return &AuthRuntime{
		Open: func(ctx context.Context) (*Handle, error) { return r.Open(ctx, data.ModeWrite) },
		Hash: hasher, Now: time.Now, Sleep: time.Sleep, R: r,
	}
}

// passwords builds the checker over the hasher and AUTH_INITIAL_PASSWORD, which
// the runtime reads from the config the db commands already carry.
func (a *AuthRuntime) passwords() auth.Passwords {
	return auth.NewPasswords(a.Hash(), a.R.Facades().Config.GetString("auth.initial_password"))
}

// Verify prompts for the dashboard password and reports whether it matched.
// An empty answer never passes, and a missing terminal refuses, because the
// prompt reads standard input.
func (a *AuthRuntime) Verify(ctx console.Context, store *Handle, reason string) (bool, error) {
	if !a.R.Terminal() {
		return false, errNoTerminal(reason)
	}
	st, err := store.Store.Settings().Effective(ctx)
	if err != nil {
		return false, err
	}
	stored, _ := st.GetString("password")
	pass := a.passwords()

	for attempt := 1; attempt <= PasswordAttempts; attempt++ {
		answer, err := ctx.Secret("Dashboard password")
		if err != nil {
			return false, err
		}
		if answer == "" {
			return false, errNoTerminal(reason)
		}
		if pass.Check(stored, answer) {
			return true, nil
		}
		if attempt < PasswordAttempts {
			ctx.Warning("Wrong password")
			a.Sleep(PasswordPause)
		}
	}
	return false, errWrongPassword
}
