package repository

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	contractsdb "github.com/goravel/framework/contracts/database/db"
	"rllmx/app/store/dialect"
)

// UserRow is one standard dashboard account. Password holds the stored hash,
// never a plain value.
type UserRow struct {
	ID          string
	Username    string
	Email       string
	Password    string
	Provisioned bool
	CreatedAt   string
	// FullName is what the dashboard shows; SSOName is the last name a provider
	// supplied, kept so an operator can re-apply it.
	FullName string
	SSOName  string
}

// UserPatch holds the fields an update may change; nil fields are left alone.
type UserPatch struct {
	Username    *string
	Email       *string
	Password    *string
	Provisioned *bool
	FullName    *string
	SSOName     *string
}

// UsersRepo reads and writes the users table and the key assignments beside it.
type UsersRepo struct{ s *Store }

// Users returns the standard-user repository.
func (s *Store) Users() *UsersRepo { return &UsersRepo{s} }

const userCols = `"id","username","password","provisioned","createdAt","fullName","ssoName"`

type userRow struct {
	ID          string         `db:"id"`
	Username    string         `db:"username"`
	Email       sql.NullString `db:"email"`
	Password    string         `db:"password"`
	Provisioned sql.NullInt64  `db:"provisioned"`
	CreatedAt   string         `db:"createdAt"`
	FullName    sql.NullString `db:"fullName"`
	SSOName     sql.NullString `db:"ssoName"`
}

func (r userRow) user() UserRow {
	return UserRow{
		ID: r.ID, Username: r.Username, Email: nullStr(r.Email), Password: r.Password,
		Provisioned: r.Provisioned.Valid && r.Provisioned.Int64 == 1, CreatedAt: r.CreatedAt,
		FullName: nullStr(r.FullName), SSOName: nullStr(r.SSOName),
	}
}

func (r *UsersRepo) one(x contractsdb.Tx, where string, args ...any) (UserRow, bool, error) {
	var rows []userRow
	query := `SELECT ` + userCols + `,"email" FROM "users" WHERE ` + where
	if err := r.s.sel(x, &rows, query, args...); err != nil {
		return UserRow{}, false, err
	}
	if len(rows) != 1 {
		return UserRow{}, false, nil
	}
	return rows[0].user(), true, nil
}

// Get returns one user by id.
func (r *UsersRepo) Get(ctx context.Context, id string) (UserRow, bool, error) {
	return r.one(r.s.on(ctx), `"id" = ?`, id)
}

// List returns every user, ordered by name.
func (r *UsersRepo) List(ctx context.Context) ([]UserRow, error) {
	var rows []userRow
	if err := r.s.sel(r.s.on(ctx), &rows, `SELECT `+userCols+`,"email" FROM "users" ORDER BY lower("username"),"id"`); err != nil {
		return nil, err
	}
	out := make([]UserRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.user())
	}
	return out, nil
}

// ErrFull is what CreateWithin reports when the table already holds the limit.
var ErrFull = errors.New("repository: the user table is full")

// Create stores a user. The caller builds the id and the hash; an empty email is
// stored NULL, so the unique index admits several users without one.
func (r *UsersRepo) Create(ctx context.Context, u UserRow) error {
	return r.insert(r.s.on(ctx), u)
}

// CreateWithin stores a user only while the table holds fewer than limit rows.
// The count and the insert share one transaction, so parallel sign-ins cannot
// each see room for one more.
func (r *UsersRepo) CreateWithin(ctx context.Context, u UserRow, limit int) error {
	return r.s.Tx(ctx, func(tx contractsdb.Tx) error {
		n, err := r.s.count(tx, `SELECT count(*) FROM "users"`)
		if err != nil {
			return err
		}
		if n >= int64(limit) {
			return ErrFull
		}
		return r.insert(tx, u)
	})
}

func (r *UsersRepo) insert(x contractsdb.Tx, u UserRow) error {
	return r.s.stmt(x, `INSERT INTO "users"(`+userCols+`,"email") VALUES(?,?,?,?,?,NULLIF(?,''),NULLIF(?,''),NULLIF(?,''))`,
		dialect.StripNUL(u.ID), dialect.StripNUL(u.Username), dialect.StripNUL(u.Password),
		dialect.BoolInt(u.Provisioned), dialect.StripNUL(u.CreatedAt),
		dialect.StripNUL(u.FullName), dialect.StripNUL(u.SSOName), strings.ToLower(dialect.StripNUL(u.Email)))
}

// Update applies a patch to one user.
func (r *UsersRepo) Update(ctx context.Context, id string, patch UserPatch) (UserRow, error) {
	var out UserRow
	err := r.s.Tx(ctx, func(tx contractsdb.Tx) error {
		u, ok, err := r.one(tx, `"id" = ?`, dialect.StripNUL(id))
		if err != nil {
			return err
		}
		if !ok {
			return ErrNotFound
		}
		if patch.Username != nil {
			u.Username = dialect.StripNUL(*patch.Username)
		}
		if patch.Email != nil {
			u.Email = strings.ToLower(dialect.StripNUL(*patch.Email))
		}
		if patch.Password != nil {
			u.Password = dialect.StripNUL(*patch.Password)
		}
		if patch.Provisioned != nil {
			u.Provisioned = *patch.Provisioned
		}
		if patch.FullName != nil {
			u.FullName = dialect.StripNUL(*patch.FullName)
		}
		if patch.SSOName != nil {
			u.SSOName = dialect.StripNUL(*patch.SSOName)
		}
		if err := r.s.stmt(tx, `UPDATE "users" SET "username"=?,"email"=NULLIF(?,''),"password"=?,"provisioned"=?,"fullName"=NULLIF(?,''),"ssoName"=NULLIF(?,'') WHERE "id"=?`,
			u.Username, u.Email, u.Password, dialect.BoolInt(u.Provisioned), u.FullName, u.SSOName, u.ID); err != nil {
			return err
		}
		out = u
		return nil
	})
	return out, err
}

// Delete removes a user and every key assignment that named them, in one
// transaction. The keys themselves stay.
func (r *UsersRepo) Delete(ctx context.Context, id string) (bool, error) {
	var removed int64
	err := r.s.Tx(ctx, func(tx contractsdb.Tx) error {
		if err := r.s.stmt(tx, `DELETE FROM "userKeys" WHERE "userId" = ?`, dialect.StripNUL(id)); err != nil {
			return err
		}
		n, err := r.s.affected(tx, `DELETE FROM "users" WHERE "id" = ?`, dialect.StripNUL(id))
		removed = n
		return err
	})
	return removed > 0, err
}

// Owners maps every assigned key id to its user.
func (r *UsersRepo) Owners(ctx context.Context) (map[string]string, error) {
	var rows []struct {
		KeyID  string `db:"keyId"`
		UserID string `db:"userId"`
	}
	if err := r.s.sel(r.s.on(ctx), &rows, `SELECT "keyId","userId" FROM "userKeys"`); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, row := range rows {
		out[row.KeyID] = row.UserID
	}
	return out, nil
}

// KeysOf returns the keys assigned to a user, sorted. A user with none gets an
// empty list, never nil: a caller reads nil as "every key".
func (r *UsersRepo) KeysOf(ctx context.Context, userID string) ([]string, error) {
	out := []string{}
	if err := r.s.sel(r.s.on(ctx), &out, `SELECT "keyId" FROM "userKeys" WHERE "userId" = ? ORDER BY "keyId"`, dialect.StripNUL(userID)); err != nil {
		return nil, err
	}
	return out, nil
}

// SetOwner assigns a key to a user, replacing any previous assignment.
func (r *UsersRepo) SetOwner(ctx context.Context, keyID, userID string) error {
	return r.s.stmt(r.s.on(ctx), `INSERT INTO "userKeys"("keyId","userId") VALUES(?,?) ON CONFLICT("keyId") DO UPDATE SET "userId"=excluded."userId"`,
		dialect.StripNUL(keyID), dialect.StripNUL(userID))
}

// ClearOwner drops a key's assignment.
func (r *UsersRepo) ClearOwner(ctx context.Context, keyID string) error {
	return r.s.stmt(r.s.on(ctx), `DELETE FROM "userKeys" WHERE "keyId" = ?`, dialect.StripNUL(keyID))
}
