package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/tidwall/gjson"

	contractsdb "github.com/goravel/framework/contracts/database/db"
	"rllmx/app/store/dialect"
	"rllmx/app/store/document"
)

// Node is a user-defined provider node. Data holds every field that has no column.
type Node struct {
	ID        string
	Type      string
	Name      string
	Data      string
	CreatedAt string
	UpdatedAt string
}

// Doc returns the node's data column for reading single fields.
func (n Node) Doc() document.Doc { return document.From(n.Data) }

// ErrPrefixTaken means another node already uses the prefix.
var ErrPrefixTaken = errors.New("repository: node prefix is already used")

// NodesRepo reads and writes the providerNodes table.
type NodesRepo struct{ s *Store }

// Nodes returns the provider node repository.
func (s *Store) Nodes() *NodesRepo { return &NodesRepo{s} }

const nodeCols = `"id","type","name","data","createdAt","updatedAt"`

type nodeRow struct {
	ID        string         `db:"id"`
	Type      sql.NullString `db:"type"`
	Name      sql.NullString `db:"name"`
	Data      string         `db:"data"`
	CreatedAt string         `db:"createdAt"`
	UpdatedAt string         `db:"updatedAt"`
}

func (r nodeRow) node() Node {
	return Node{ID: r.ID, Type: nullStr(r.Type), Name: nullStr(r.Name), Data: r.Data, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}

func (r *NodesRepo) get(x contractsdb.Tx, id string) (Node, bool, error) {
	var rows []nodeRow
	if err := r.s.sel(x, &rows, `SELECT `+nodeCols+` FROM "providerNodes" WHERE "id" = ?`, dialect.StripNUL(id)); err != nil {
		return Node{}, false, err
	}
	if len(rows) == 0 {
		return Node{}, false, nil
	}
	return rows[0].node(), true, nil
}

// Get finds a node by id.
func (r *NodesRepo) Get(ctx context.Context, id string) (Node, bool, error) {
	return r.get(r.s.on(ctx), id)
}

// List returns the nodes of a type; an empty type lists all.
func (r *NodesRepo) List(ctx context.Context, typ string) ([]Node, error) {
	q, args := `SELECT `+nodeCols+` FROM "providerNodes"`, []any{}
	if typ != "" {
		q, args = q+` WHERE "type" = ?`, append(args, dialect.StripNUL(typ))
	}
	var rows []nodeRow
	if err := r.s.sel(r.s.on(ctx), &rows, q+` ORDER BY "createdAt","id"`, args...); err != nil {
		return nil, err
	}
	out := make([]Node, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.node())
	}
	return out, nil
}

// CreateUnique inserts or replaces a node, failing with ErrPrefixTaken when
// another node uses prefix. Check and write share one transaction, so
// concurrent requests cannot both pass.
func (r *NodesRepo) CreateUnique(ctx context.Context, n Node, prefix string) (Node, error) {
	if _, err := document.ParseDoc(n.Data); err != nil {
		return Node{}, fmt.Errorf("node data: %w", err)
	}
	if n.ID == "" {
		n.ID = dialect.NewID()
	}
	ts := dialect.Now()
	var out Node
	err := r.s.Tx(ctx, func(tx contractsdb.Tx) error {
		if err := r.prefixFree(tx, prefix, n.ID); err != nil {
			return err
		}
		if err := r.s.stmt(tx, `INSERT INTO "providerNodes"(`+nodeCols+`) VALUES(?,?,?,?,?,?)
			ON CONFLICT("id") DO UPDATE SET "type"=excluded."type","name"=excluded."name","data"=excluded."data","updatedAt"=excluded."updatedAt"`,
			dialect.StripNUL(n.ID), dialect.StripNUL(n.Type), dialect.StripNUL(n.Name), dialect.StripNUL(n.Data), ts, ts); err != nil {
			return err
		}
		var err error
		out, _, err = r.get(tx, dialect.StripNUL(n.ID))
		return err
	})
	return out, err
}

// prefixFree fails when another node uses the prefix. An empty prefix means the
// node has none, so it never collides.
func (r *NodesRepo) prefixFree(tx contractsdb.Tx, prefix, self string) error {
	if prefix == "" {
		return nil
	}
	var rows []nodeRow
	if err := r.s.sel(tx, &rows, `SELECT `+nodeCols+` FROM "providerNodes" WHERE "id" <> ?`, dialect.StripNUL(self)); err != nil {
		return err
	}
	for _, row := range rows {
		if prefixOf(row.Data) == prefix {
			return ErrPrefixTaken
		}
	}
	return nil
}

// prefixOf reads the prefix stored in a node's data; anything unreadable is "".
func prefixOf(data string) string {
	v := gjson.Get(data, "prefix")
	if v.Type != gjson.String {
		return ""
	}
	return v.String()
}

// Update merges keys into a node. The keys "name" and "type" set their columns;
// everything else goes into the data document.
func (r *NodesRepo) Update(ctx context.Context, id string, patch map[string]string) (Node, error) {
	var out Node
	err := r.s.Tx(ctx, func(tx contractsdb.Tx) error {
		n, err := r.updateIn(tx, id, patch)
		out = n
		return err
	})
	return out, err
}

// UpdateCascade is Update plus a merge of connPSD into every connection of the
// node, in one transaction. It fails with ErrPrefixTaken when another node
// uses prefix.
func (r *NodesRepo) UpdateCascade(ctx context.Context, id string, patch, connPSD map[string]string, prefix string) (Node, error) {
	var out Node
	err := r.s.Tx(ctx, func(tx contractsdb.Tx) error {
		if err := r.prefixFree(tx, prefix, id); err != nil {
			return err
		}
		n, err := r.updateIn(tx, id, patch)
		if err != nil {
			return err
		}
		out = n
		conns := &ConnectionsRepo{r.s}
		list, err := conns.rows(tx, ` WHERE "provider" = ?`, dialect.StripNUL(id))
		if err != nil {
			return err
		}
		for _, c := range list {
			cc, d, err := conns.lockRow(tx, c.ID)
			if err != nil {
				return err
			}
			psd, err := patchObject(d, "providerSpecificData", connPSD, nil)
			if err != nil {
				return err
			}
			if err := d.SetRaw("providerSpecificData", psd); err != nil {
				return err
			}
			if _, err := conns.writeData(tx, cc, d); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

func (r *NodesRepo) updateIn(tx contractsdb.Tx, id string, patch map[string]string) (Node, error) {
	n, ok, err := r.get(tx, id)
	if err != nil {
		return Node{}, err
	}
	if !ok {
		return Node{}, ErrNotFound
	}
	d, err := document.ParseDoc(n.Data)
	if err != nil {
		return Node{}, fmt.Errorf("node %s: %w", id, err)
	}
	for _, k := range sortedKeys(patch) {
		v := patch[k]
		if !gjson.Valid(v) {
			return Node{}, fmt.Errorf("repository: node value for %q is not valid JSON", k)
		}
		switch k {
		case "name":
			n.Name = gjson.Parse(v).String()
		case "type":
			n.Type = gjson.Parse(v).String()
		default:
			if err := d.SetRaw(k, v); err != nil {
				return Node{}, err
			}
		}
	}
	n.Data, n.UpdatedAt = d.String(), dialect.Now()
	err = r.s.stmt(tx, `UPDATE "providerNodes" SET "type"=?,"name"=?,"data"=?,"updatedAt"=? WHERE "id"=?`,
		dialect.StripNUL(n.Type), dialect.StripNUL(n.Name), dialect.StripNUL(n.Data), n.UpdatedAt, n.ID)
	return n, err
}

// DeleteCascade removes a node, its connections and its custom models in one
// transaction and returns how many connections went with it.
func (r *NodesRepo) DeleteCascade(ctx context.Context, id string) (int64, error) {
	var removed int64
	err := r.s.Tx(ctx, func(tx contractsdb.Tx) error {
		if _, ok, err := r.get(tx, id); err != nil {
			return err
		} else if !ok {
			return ErrNotFound
		}
		var err error
		if removed, err = r.s.affected(tx, `DELETE FROM "providerConnections" WHERE "provider" = ?`, dialect.StripNUL(id)); err != nil {
			return err
		}
		return r.deleteIn(tx, id)
	})
	return removed, err
}

func (r *NodesRepo) deleteIn(tx contractsdb.Tx, id string) error {
	id = dialect.StripNUL(id)
	prefix := id + "|"
	if err := r.s.stmt(tx, `DELETE FROM "providerNodes" WHERE "id" = ?`, id); err != nil {
		return err
	}
	return r.s.stmt(tx, `DELETE FROM "kv" WHERE "scope" = ? AND substr("key", 1, ?) = ?`,
		ScopeCustomModels, int64(utf8.RuneCountInString(prefix)), prefix)
}
