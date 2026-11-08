package repository

import (
	"context"
	"database/sql"
	"strings"

	"github.com/tidwall/gjson"

	contractsdb "github.com/goravel/framework/contracts/database/db"
	"rllmx/app/dialect"
)

// KeyAccess limits which models a key may call.
type KeyAccess struct {
	Restricted bool
	Allow      []string
}

// APIKey is one stored client key.
type APIKey struct {
	ID               string
	Key              string
	Name             string
	MachineID        string
	IsActive         bool
	CreatedAt        string
	AccessRestricted bool
	AccessAllow      []string
	// Unreadable marks a key whose stored value does not open; Key is then empty.
	Unreadable bool
}

// APIKeyPatch holds the fields an update may change; nil fields are left alone.
type APIKeyPatch struct {
	Name     *string
	IsActive *bool
	Access   *KeyAccess
}

// APIKeysRepo reads and writes the apiKeys table.
type APIKeysRepo struct{ s *Store }

// APIKeys returns the API key repository.
func (s *Store) APIKeys() *APIKeysRepo { return &APIKeysRepo{s} }

const keyCols = `"id","key","name","machineId","isActive","createdAt","accessRestricted","accessAllow"`

type keyRow struct {
	ID               string         `db:"id"`
	Key              string         `db:"key"`
	Name             sql.NullString `db:"name"`
	MachineID        sql.NullString `db:"machineId"`
	IsActive         sql.NullInt64  `db:"isActive"`
	CreatedAt        string         `db:"createdAt"`
	AccessRestricted sql.NullInt64  `db:"accessRestricted"`
	AccessAllow      sql.NullString `db:"accessAllow"`
}

// normalizeAllow trims entries, drops empties and de-duplicates
// case-insensitively, keeping the first spelling and the order.
func normalizeAllow(list []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, e := range list {
		e = strings.TrimSpace(e)
		if e == "" || seen[strings.ToLower(e)] {
			continue
		}
		seen[strings.ToLower(e)] = true
		out = append(out, e)
	}
	return out
}

func (s *Store) keyFromRow(r keyRow) APIKey {
	k := APIKey{ID: r.ID, Key: r.Key, CreatedAt: r.CreatedAt, Name: nullStr(r.Name), MachineID: nullStr(r.MachineID)}
	if plain, err := s.OpenAPIKey(r.Key); err != nil {
		k.Key, k.Unreadable = "", true
	} else {
		k.Key = plain
	}
	k.IsActive = r.IsActive.Valid && r.IsActive.Int64 == 1
	k.AccessRestricted = r.AccessRestricted.Valid && r.AccessRestricted.Int64 == 1
	k.AccessAllow = []string{}
	// A corrupt allow list can only shrink a restricted key to nothing.
	if k.AccessRestricted && r.AccessAllow.Valid && gjson.Valid(r.AccessAllow.String) {
		k.AccessAllow = normalizeAllow(decodeStrings(r.AccessAllow.String))
	}
	return k
}

func accessColumns(a KeyAccess) (int64, string) {
	if !a.Restricted {
		return 0, "[]"
	}
	return 1, encodeStrings(a.Allow)
}

// one reads a key by a column. Selecting into a slice keeps a lookup by a
// presented key from logging it: a struct destination logs the bound
// arguments when no row is found.
func (r *APIKeysRepo) one(x contractsdb.Tx, col, arg string) (APIKey, bool, error) {
	var rows []keyRow
	if err := r.s.sel(x, &rows, `SELECT `+keyCols+` FROM "apiKeys" WHERE `+col+` = ?`, dialect.StripNUL(arg)); err != nil {
		return APIKey{}, false, err
	}
	if len(rows) == 0 {
		return APIKey{}, false, nil
	}
	return r.s.keyFromRow(rows[0]), true, nil
}

// GetByKey finds a key by its plain text.
func (r *APIKeysRepo) GetByKey(ctx context.Context, key string) (APIKey, bool, error) {
	sealed, err := r.s.SealAPIKey(key)
	if err != nil {
		return APIKey{}, false, err
	}
	return r.one(r.s.on(ctx), `"key"`, sealed)
}

// GetByID finds a key by id.
func (r *APIKeysRepo) GetByID(ctx context.Context, id string) (APIKey, bool, error) {
	return r.one(r.s.on(ctx), `"id"`, id)
}

// List returns keys oldest first.
func (r *APIKeysRepo) List(ctx context.Context) ([]APIKey, error) {
	var rows []keyRow
	if err := r.s.sel(r.s.on(ctx), &rows, `SELECT `+keyCols+` FROM "apiKeys" ORDER BY "createdAt","id"`); err != nil {
		return nil, err
	}
	out := make([]APIKey, 0, len(rows))
	for _, row := range rows {
		out = append(out, r.s.keyFromRow(row))
	}
	return out, nil
}

// Create stores a new unrestricted, active key. The caller builds the key string.
func (r *APIKeysRepo) Create(ctx context.Context, name, machineID, key string) (APIKey, error) {
	k := APIKey{ID: dialect.NewID(), Key: dialect.StripNUL(key), Name: dialect.StripNUL(name), MachineID: dialect.StripNUL(machineID), IsActive: true, CreatedAt: dialect.Now(), AccessAllow: []string{}}
	sealed, err := r.s.SealAPIKey(k.Key)
	if err != nil {
		return APIKey{}, err
	}
	err = r.s.stmt(r.s.on(ctx), `INSERT INTO "apiKeys"(`+keyCols+`) VALUES(?,?,?,?,?,?,?,?)`,
		k.ID, sealed, k.Name, k.MachineID, int64(1), k.CreatedAt, int64(0), "[]")
	if err != nil {
		return APIKey{}, err
	}
	return k, nil
}

// Update applies a patch to one key.
func (r *APIKeysRepo) Update(ctx context.Context, id string, patch APIKeyPatch) (APIKey, error) {
	var out APIKey
	err := r.s.Tx(ctx, func(tx contractsdb.Tx) error {
		k, ok, err := r.one(tx, `"id"`, id)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNotFound
		}
		if patch.Name != nil {
			k.Name = dialect.StripNUL(*patch.Name)
		}
		if patch.IsActive != nil {
			k.IsActive = *patch.IsActive
		}
		if patch.Access != nil {
			k.AccessRestricted = patch.Access.Restricted
			k.AccessAllow = normalizeAllow(patch.Access.Allow)
			if !k.AccessRestricted {
				k.AccessAllow = []string{}
			}
		}
		restricted, allow := accessColumns(KeyAccess{Restricted: k.AccessRestricted, Allow: k.AccessAllow})
		if patch.Access == nil && k.AccessRestricted {
			// Rewriting the access columns would erase a list that failed to parse.
			err = r.s.stmt(tx, `UPDATE "apiKeys" SET "name"=?,"isActive"=? WHERE "id"=?`, k.Name, dialect.BoolInt(k.IsActive), k.ID)
		} else {
			err = r.s.stmt(tx, `UPDATE "apiKeys" SET "name"=?,"isActive"=?,"accessRestricted"=?,"accessAllow"=? WHERE "id"=?`,
				k.Name, dialect.BoolInt(k.IsActive), restricted, allow, k.ID)
		}
		out = k
		return err
	})
	return out, err
}

// Delete removes a key and reports whether it existed.
func (r *APIKeysRepo) Delete(ctx context.Context, id string) (bool, error) {
	n, err := r.s.affected(r.s.on(ctx), `DELETE FROM "apiKeys" WHERE "id" = ?`, dialect.StripNUL(id))
	return n > 0, err
}
