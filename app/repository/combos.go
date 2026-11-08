package repository

import (
	"context"
	"database/sql"

	contractsdb "github.com/goravel/framework/contracts/database/db"
	"rllmx/app/dialect"
)

// Combo is a named model list. Kind stays empty for plain combos.
type Combo struct {
	ID        string
	Name      string
	Kind      string
	Models    []string
	CreatedAt string
	UpdatedAt string
}

// CombosRepo reads and writes the combos table.
type CombosRepo struct{ s *Store }

// Combos returns the combo repository.
func (s *Store) Combos() *CombosRepo { return &CombosRepo{s} }

const comboCols = `"id","name","kind","models","createdAt","updatedAt"`

type comboRow struct {
	ID        string         `db:"id"`
	Name      string         `db:"name"`
	Kind      sql.NullString `db:"kind"`
	Models    string         `db:"models"`
	CreatedAt string         `db:"createdAt"`
	UpdatedAt string         `db:"updatedAt"`
}

func (r comboRow) combo() Combo {
	return Combo{ID: r.ID, Name: r.Name, Kind: nullStr(r.Kind), Models: decodeStrings(r.Models), CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}

func (r *CombosRepo) byID(x contractsdb.Tx, id string) (Combo, bool, error) {
	var rows []comboRow
	if err := r.s.sel(x, &rows, `SELECT `+comboCols+` FROM "combos" WHERE "id" = ?`, dialect.StripNUL(id)); err != nil {
		return Combo{}, false, err
	}
	if len(rows) == 0 {
		return Combo{}, false, nil
	}
	return rows[0].combo(), true, nil
}

// List returns combos in creation order.
func (r *CombosRepo) List(ctx context.Context) ([]Combo, error) {
	var rows []comboRow
	if err := r.s.sel(r.s.on(ctx), &rows, `SELECT `+comboCols+` FROM "combos" ORDER BY "createdAt","id"`); err != nil {
		return nil, err
	}
	out := make([]Combo, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.combo())
	}
	return out, nil
}

// GetByID finds a combo by id.
func (r *CombosRepo) GetByID(ctx context.Context, id string) (Combo, bool, error) {
	return r.byID(r.s.on(ctx), id)
}

// Create inserts a combo; a duplicate name fails.
func (r *CombosRepo) Create(ctx context.Context, name string, models []string) (Combo, error) {
	if models == nil {
		models = []string{}
	}
	ts := dialect.Now()
	c := Combo{ID: dialect.NewID(), Name: dialect.StripNUL(name), Models: models, CreatedAt: ts, UpdatedAt: ts}
	err := r.s.stmt(r.s.on(ctx), `INSERT INTO "combos"(`+comboCols+`) VALUES(?,?,?,?,?,?)`,
		c.ID, c.Name, nil, encodeStrings(models), ts, ts)
	if err != nil {
		return Combo{}, err
	}
	return c, nil
}

// Update replaces a combo's name and members.
func (r *CombosRepo) Update(ctx context.Context, id, name string, models []string) (Combo, error) {
	if models == nil {
		models = []string{}
	}
	var out Combo
	err := r.s.Tx(ctx, func(tx contractsdb.Tx) error {
		c, ok, err := r.byID(tx, id)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNotFound
		}
		c.Name, c.Models, c.UpdatedAt = dialect.StripNUL(name), models, dialect.Now()
		if err := r.s.stmt(tx, `UPDATE "combos" SET "name"=?,"models"=?,"updatedAt"=? WHERE "id"=?`,
			c.Name, encodeStrings(models), c.UpdatedAt, c.ID); err != nil {
			return err
		}
		out = c
		return nil
	})
	return out, err
}

// Delete removes a combo and reports whether it existed.
func (r *CombosRepo) Delete(ctx context.Context, id string) (bool, error) {
	n, err := r.s.affected(r.s.on(ctx), `DELETE FROM "combos" WHERE "id" = ?`, dialect.StripNUL(id))
	return n > 0, err
}
