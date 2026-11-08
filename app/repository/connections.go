package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"

	contractsdb "github.com/goravel/framework/contracts/database/db"
	"rllmx/app/dialect"
	"rllmx/app/document"
)

const (
	modelLockPrefix = "modelLock_"
	accountLockKey  = "modelLock___all"
	maxReasonRunes  = 200

	// StatusExpired is what the `testStatus` field of a connection's data holds
	// when its refresh token was refused for good. Account selection skips it
	// until it is reconnected.
	StatusExpired = "expired"
)

// connectionFields are the data keys a new connection may carry; anything
// else a caller sends is dropped.
var connectionFields = []string{
	"displayName", "globalPriority", "defaultModel", "accessToken", "refreshToken", "expiresAt", "tokenType",
	"scope", "projectId", "apiKey", "testStatus", "lastTested", "lastError", "lastErrorAt", "rateLimitedUntil",
	"expiresIn", "errorCode", "consecutiveUseCount", "idToken", "lastRefreshAt",
}

// Connection is a provider account. Data holds every field that has no column.
type Connection struct {
	ID        string
	Provider  string
	AuthType  string
	Name      string
	Email     string
	Priority  int64
	IsActive  bool
	Data      string
	CreatedAt string
	UpdatedAt string
	// Corrupt marks a row whose data column is not a valid JSON object or whose
	// sealed fields do not open; such a row is returned as stored and never
	// written.
	Corrupt bool
	// stored is the data column as read, with the secrets still sealed.
	stored string
}

// document.Doc returns the data column for reading single fields.
func (c Connection) Doc() document.Doc { return document.From(c.Data) }

// ConnInput describes a connection to create. Fields and PSD hold raw JSON values.
type ConnInput struct {
	Provider string
	AuthType string
	Name     string
	Email    string
	IsActive *bool
	Fields   map[string]string
	PSD      string
}

// ConnFilter narrows a connection listing.
type ConnFilter struct {
	Provider string
	IsActive *bool
}

// ConnPatch is a partial update; nil and empty fields are left alone.
type ConnPatch struct {
	Name     *string
	Email    *string
	IsActive *bool
	Priority *int64
	Fields   map[string]string
}

// ConnectionsRepo reads and writes the providerConnections table.
type ConnectionsRepo struct{ s *Store }

// Connections returns the connection repository.
func (s *Store) Connections() *ConnectionsRepo { return &ConnectionsRepo{s} }

const connCols = `"id","provider","authType","name","email","priority","isActive","data","createdAt","updatedAt"`

type connRow struct {
	ID        string         `db:"id"`
	Provider  string         `db:"provider"`
	AuthType  string         `db:"authType"`
	Name      sql.NullString `db:"name"`
	Email     sql.NullString `db:"email"`
	Priority  sql.NullInt64  `db:"priority"`
	IsActive  sql.NullInt64  `db:"isActive"`
	Data      string         `db:"data"`
	CreatedAt string         `db:"createdAt"`
	UpdatedAt string         `db:"updatedAt"`
}

func (s *Store) connFromRow(r connRow) Connection {
	c := Connection{
		ID: r.ID, Provider: r.Provider, AuthType: r.AuthType, Name: nullStr(r.Name), Email: nullStr(r.Email),
		Priority: r.Priority.Int64, IsActive: r.IsActive.Valid && r.IsActive.Int64 == 1,
		Data: r.Data, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, stored: r.Data,
	}
	plain, opened := s.OpenConnectionData(c.ID, c.Data)
	if !opened {
		c.Corrupt = true
		return c
	}
	c.Data = plain
	_, err := document.ParseDoc(c.Data)
	c.Corrupt = err != nil
	return c
}

func sortPriority(p int64) int64 {
	if p == 0 {
		return 999
	}
	return p
}

func (r *ConnectionsRepo) rows(x contractsdb.Tx, where string, args ...any) ([]Connection, error) {
	var rows []connRow
	if err := r.s.sel(x, &rows, `SELECT `+connCols+` FROM "providerConnections"`+where, args...); err != nil {
		return nil, err
	}
	out := make([]Connection, 0, len(rows))
	for _, row := range rows {
		out = append(out, r.s.connFromRow(row))
	}
	return out, nil
}

// List returns connections ordered by priority; unset priorities sort last.
func (r *ConnectionsRepo) List(ctx context.Context, f ConnFilter) ([]Connection, error) {
	var conds []string
	var args []any
	if f.Provider != "" {
		conds, args = append(conds, `"provider" = ?`), append(args, dialect.StripNUL(f.Provider))
	}
	if f.IsActive != nil {
		conds, args = append(conds, `"isActive" = ?`), append(args, dialect.BoolInt(*f.IsActive))
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	list, err := r.rows(r.s.on(ctx), where, args...)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(list, func(i, j int) bool { return sortPriority(list[i].Priority) < sortPriority(list[j].Priority) })
	return list, nil
}

// Get finds a connection by id.
func (r *ConnectionsRepo) Get(ctx context.Context, id string) (Connection, bool, error) {
	list, err := r.rows(r.s.on(ctx), ` WHERE "id" = ?`, dialect.StripNUL(id))
	if err != nil || len(list) == 0 {
		return Connection{}, false, err
	}
	return list[0], true, nil
}

// lockRow reads a row inside tx for a write. A corrupt row is refused.
func (r *ConnectionsRepo) lockRow(tx contractsdb.Tx, id string) (Connection, document.Doc, error) {
	list, err := r.rows(tx, ` WHERE "id" = ?`, dialect.StripNUL(id))
	if err != nil {
		return Connection{}, document.Doc{}, err
	}
	if len(list) == 0 {
		return Connection{}, document.Doc{}, ErrNotFound
	}
	if list[0].Corrupt {
		return list[0], document.From(list[0].Data), fmt.Errorf("connection %s: %w", id, document.ErrCorruptRow)
	}
	d, err := document.ParseDoc(list[0].Data)
	if err != nil {
		return list[0], d, fmt.Errorf("connection %s: %w", id, document.ErrCorruptRow)
	}
	return list[0], d, nil
}

func (r *ConnectionsRepo) writeData(tx contractsdb.Tx, c Connection, d document.Doc) (Connection, error) {
	box, err := r.s.sealer()
	if err != nil {
		return c, err
	}
	c.Data, c.UpdatedAt = d.String(), dialect.Now()
	sealed, err := sealFields(box, dialect.StripNUL(c.Data), c.stored, connSecretPaths, connAAD(c.ID))
	if err != nil {
		return c, err
	}
	err = r.s.stmt(tx, `UPDATE "providerConnections" SET "name"=?,"email"=?,"priority"=?,"isActive"=?,"data"=?,"updatedAt"=? WHERE "id"=?`,
		dialect.NullText(c.Name), dialect.NullText(c.Email), nullInt(c.Priority), dialect.BoolInt(c.IsActive), sealed, c.UpdatedAt, c.ID)
	c.stored = sealed
	return c, err
}

func nullInt(n int64) any {
	if n == 0 {
		return nil
	}
	return n
}

// applyFields sets raw JSON values; activating an account (testStatus
// "active") also clears its error state and every model lock.
func applyFields(d *document.Doc, fields map[string]string) error {
	for _, k := range sortedKeys(fields) {
		if err := d.SetRaw(k, fields[k]); err != nil {
			return err
		}
	}
	status, ok := fields["testStatus"]
	if !ok || gjson.Parse(status).Type != gjson.String || gjson.Parse(status).String() != "active" {
		return nil
	}
	for _, k := range []string{"lastError", "lastErrorAt"} {
		if _, set := fields[k]; !set {
			if err := d.SetNull(k); err != nil {
				return err
			}
		}
	}
	for _, k := range []string{"errorCode", "rateLimitedUntil"} {
		if err := d.SetNull(k); err != nil {
			return err
		}
	}
	if err := d.SetInt("backoffLevel", 0); err != nil {
		return err
	}
	for _, k := range d.Keys() {
		if strings.HasPrefix(k, modelLockPrefix) {
			if err := d.SetNull(k); err != nil {
				return err
			}
		}
	}
	return nil
}

func psdOf(c Connection) gjson.Result { return gjson.Get(c.Data, "providerSpecificData") }

// sameIdentity decides whether an OAuth login belongs to an existing row with the same email.
func sameIdentity(provider string, incoming gjson.Result, existing Connection) bool {
	inWs, exWs := incoming.Get("chatgptAccountId").String(), psdOf(existing).Get("chatgptAccountId").String()
	if provider == "codex" {
		return inWs != "" && exWs != "" && inWs == exWs
	}
	if inWs != "" && exWs != "" {
		return inWs == exWs
	}
	if inWs != "" || exWs != "" {
		return false
	}
	inUser, exUser := incoming.Get("username").String(), psdOf(existing).Get("username").String()
	if inUser != "" && exUser != "" {
		return inUser == exUser
	}
	return inUser == "" && exUser == ""
}

func connectionName(in ConnInput, psd gjson.Result, poolSize int64) string {
	if in.Name != "" {
		return in.Name
	}
	if in.AuthType != "oauth" && in.AuthType != "access_token" {
		return ""
	}
	fallback := in.Email
	if fallback == "" {
		fallback = "Account " + strconv.FormatInt(poolSize+1, 10)
	}
	if in.Provider == "github" {
		for _, v := range []string{psd.Get("githubLogin").String(), psd.Get("githubEmail").String(), in.Email, psd.Get("githubName").String()} {
			if v != "" {
				return v
			}
		}
	}
	return fallback
}

// applyCreate sets the supplied fields, and the provider data when it has keys.
func applyCreate(d *document.Doc, fields map[string]string, psd gjson.Result) error {
	if err := applyFields(d, fields); err != nil {
		return err
	}
	if psd.IsObject() && len(psd.Map()) > 0 {
		return d.SetRaw("providerSpecificData", psd.Raw)
	}
	return nil
}

type poolStats struct {
	Size    int64 `db:"size"`
	MaxPrio int64 `db:"maxPrio"`
}

// Create adds a connection, or updates the one it duplicates: an OAuth login
// with the same identity, or an API key with the same provider and name.
// allowOverwrite false turns the API-key case into an ErrNameConflict.
func (r *ConnectionsRepo) Create(ctx context.Context, in ConnInput, allowOverwrite *bool) (Connection, error) {
	if _, err := r.s.sealer(); err != nil {
		return Connection{}, err
	}
	if in.AuthType == "" {
		in.AuthType = "oauth"
	}
	var psd gjson.Result
	if in.PSD != "" {
		if !gjson.Valid(in.PSD) || !gjson.Parse(in.PSD).IsObject() {
			return Connection{}, errors.New("repository: providerSpecificData is not a JSON object")
		}
		psd = gjson.Parse(in.PSD)
	}
	fields := map[string]string{}
	for _, f := range connectionFields {
		if v, ok := in.Fields[f]; ok && v != "null" {
			fields[f] = v
		}
	}
	for _, v := range fields {
		if !gjson.Valid(v) {
			return Connection{}, errors.New("repository: connection field is not valid JSON")
		}
	}
	var out Connection
	err := r.s.Tx(ctx, func(tx contractsdb.Tx) error {
		var candidates []Connection
		var err error
		switch {
		case in.AuthType == "oauth" && in.Email != "":
			candidates, err = r.rows(tx, ` WHERE "provider" = ? AND "authType" = 'oauth' AND "email" = ? ORDER BY "createdAt","id"`, dialect.StripNUL(in.Provider), dialect.StripNUL(in.Email))
		case in.AuthType == "apikey" && in.Name != "":
			candidates, err = r.rows(tx, ` WHERE "provider" = ? AND "authType" = 'apikey' AND "name" = ? ORDER BY "createdAt","id"`, dialect.StripNUL(in.Provider), dialect.StripNUL(in.Name))
		}
		if err != nil {
			return err
		}
		for _, c := range candidates {
			if c.Corrupt || (in.AuthType == "oauth" && !sameIdentity(in.Provider, psd, c)) {
				continue
			}
			if in.AuthType == "apikey" && allowOverwrite != nil && !*allowOverwrite {
				return &ErrNameConflict{ExistingID: c.ID, ExistingName: c.Name}
			}
			d, _ := document.ParseDoc(c.Data)
			if err := applyCreate(&d, fields, psd); err != nil {
				return err
			}
			if in.IsActive != nil {
				c.IsActive = *in.IsActive
			}
			out, err = r.writeData(tx, c, d)
			return err
		}

		var stats []poolStats
		if err := r.s.sel(tx, &stats, `SELECT COUNT(*) AS size, COALESCE(MAX("priority"),0) AS maxPrio FROM "providerConnections" WHERE "provider" = ?`, dialect.StripNUL(in.Provider)); err != nil {
			return err
		}
		if len(stats) != 1 {
			return fmt.Errorf("repository: pool statistics returned %d rows", len(stats))
		}
		d, _ := document.ParseDoc("{}")
		if err := applyCreate(&d, fields, psd); err != nil {
			return err
		}
		ts := dialect.Now()
		out = Connection{
			ID: dialect.NewID(), Provider: dialect.StripNUL(in.Provider), AuthType: dialect.StripNUL(in.AuthType),
			Name: connectionName(in, psd, stats[0].Size), Email: in.Email, Priority: stats[0].MaxPrio + 1, IsActive: in.IsActive == nil || *in.IsActive,
			Data: d.String(), CreatedAt: ts, UpdatedAt: ts,
		}
		sealed, err := r.s.SealConnectionData(out.ID, dialect.StripNUL(out.Data))
		if err != nil {
			return err
		}
		out.stored = sealed
		return r.s.stmt(tx, `INSERT INTO "providerConnections"(`+connCols+`) VALUES(?,?,?,?,?,?,?,?,?,?)`,
			out.ID, out.Provider, out.AuthType, dialect.NullText(out.Name), dialect.NullText(out.Email), out.Priority, dialect.BoolInt(out.IsActive), sealed, ts, ts)
	})
	return out, err
}

// Update merges a patch into a connection, touching only the patched keys of its data.
func (r *ConnectionsRepo) Update(ctx context.Context, id string, p ConnPatch) (Connection, error) {
	var out Connection
	err := r.s.Tx(ctx, func(tx contractsdb.Tx) error {
		c, d, err := r.lockRow(tx, id)
		if err != nil {
			return err
		}
		if err := applyFields(&d, p.Fields); err != nil {
			return err
		}
		if p.Name != nil {
			c.Name = *p.Name
		}
		if p.Email != nil {
			c.Email = *p.Email
		}
		if p.IsActive != nil {
			c.IsActive = *p.IsActive
		}
		if p.Priority != nil {
			c.Priority = *p.Priority
		}
		if out, err = r.writeData(tx, c, d); err != nil {
			return err
		}
		if p.Priority != nil {
			return r.reorder(tx, c.Provider, c.ID)
		}
		return nil
	})
	return out, err
}

// reorder renumbers a provider's priorities to 1..N. Ties go to the most
// recently updated row, and then to prefer.
func (r *ConnectionsRepo) reorder(tx contractsdb.Tx, provider, prefer string) error {
	list, err := r.rows(tx, ` WHERE "provider" = ?`, provider)
	if err != nil {
		return err
	}
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		if a.UpdatedAt != b.UpdatedAt {
			return a.UpdatedAt > b.UpdatedAt
		}
		if (a.ID == prefer) != (b.ID == prefer) {
			return a.ID == prefer
		}
		if a.CreatedAt != b.CreatedAt {
			return a.CreatedAt < b.CreatedAt
		}
		return a.ID < b.ID
	})
	for i, c := range list {
		if want := int64(i + 1); c.Priority != want {
			if err := r.s.stmt(tx, `UPDATE "providerConnections" SET "priority" = ? WHERE "id" = ?`, want, c.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// Delete removes a connection and closes the gap in its provider's priorities.
func (r *ConnectionsRepo) Delete(ctx context.Context, id string) (bool, error) {
	found := false
	err := r.s.Tx(ctx, func(tx contractsdb.Tx) error {
		var provider []string
		if err := r.s.sel(tx, &provider, `SELECT "provider" FROM "providerConnections" WHERE "id" = ?`, dialect.StripNUL(id)); err != nil {
			return err
		}
		if len(provider) == 0 {
			return nil
		}
		if err := r.s.stmt(tx, `DELETE FROM "providerConnections" WHERE "id" = ?`, dialect.StripNUL(id)); err != nil {
			return err
		}
		found = true
		return r.reorder(tx, provider[0], "")
	})
	return found, err
}

// Count returns the number of connections of every provider, active or not.
func (r *ConnectionsRepo) Count(ctx context.Context) (int64, error) {
	return r.s.count(r.s.on(ctx), `SELECT COUNT(*) FROM "providerConnections"`)
}
