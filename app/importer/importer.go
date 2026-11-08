// Package importer seeds an empty database from a legacy one: the configuration
// that matters, without the old deployment's usage, login policy or dashboard
// password. It reads through a copy, so the source is never written.
package importer

import (
	"context"
	"errors"
	"fmt"
	"rllmx/app/document"
	"sort"
	"strings"

	contractsdb "github.com/goravel/framework/contracts/database/db"
	"github.com/tidwall/gjson"

	"rllmx/app/repository"
	"rllmx/app/sealer"
)

// Options tunes what an import copies.
type Options struct {
	// WithSSO also copies the login policy (authMode, OIDC and SAML settings).
	WithSSO bool
}

// Counts is the number of rows per kind in a Plan.
type Counts struct {
	Connections    int
	APIKeys        int
	Nodes          int
	Combos         int
	ModelAliases   int
	CustomModels   int
	DisabledModels int
	Pricing        int
	Settings       int
}

// KV is the number of kv rows across the four copied scopes.
func (c Counts) KV() int { return c.ModelAliases + c.CustomModels + c.DisabledModels + c.Pricing }

// Skipped records what was left behind and why, by count or key name only.
type Skipped struct {
	CorruptConnections int
	CorruptNodes       int
	CorruptKV          int
	UnknownSettings    []string
	SecuritySettings   []string
}

type connRow struct {
	id, provider, authType, name, email, data, createdAt, updatedAt string
	priority, isActive                                              *int64
	nameNull, emailNull                                             bool
}

type keyRow struct {
	id, key, name, machineID, allow, createdAt string
	isActive, restricted                       *int64
	nameNull, machineNull, allowNull           bool
}

type nodeRow struct {
	id, typ, name, data, createdAt, updatedAt string
	typNull, nameNull                         bool
}

type comboRow struct {
	id, name, kind, models, createdAt, updatedAt string
	kindNull                                     bool
}

type kvRow struct{ scope, key, value string }

type settingRow struct{ key, raw string }

// Plan is everything that would be written. Read builds it without touching the
// destination.
type Plan struct {
	connections []connRow
	apiKeys     []keyRow
	nodes       []nodeRow
	combos      []comboRow
	kv          []kvRow
	settings    []settingRow
	Skipped     Skipped
}

// Counts reports what the plan holds, per kind, so the command can compare it with what was written.
func (p *Plan) Counts() Counts {
	c := Counts{
		Connections: len(p.connections), APIKeys: len(p.apiKeys), Nodes: len(p.nodes),
		Combos: len(p.combos), Settings: len(p.settings),
	}
	for _, r := range p.kv {
		switch r.scope {
		case repository.ScopeModelAliases:
			c.ModelAliases++
		case repository.ScopeCustomModels:
			c.CustomModels++
		case repository.ScopeDisabledModels:
			c.DisabledModels++
		case repository.ScopePricing:
			c.Pricing++
		}
	}
	return c
}

// Report describes the plan with counts and setting names only, never a value.
func (p *Plan) Report() string {
	c := p.Counts()
	var b strings.Builder
	fmt.Fprintf(&b, "%d provider connections, %d API keys, %d provider nodes, %d combos\n", c.Connections, c.APIKeys, c.Nodes, c.Combos)
	fmt.Fprintf(&b, "%d model aliases, %d custom models, %d disabled-model lists, %d pricing overrides, %d settings\n", c.ModelAliases, c.CustomModels, c.DisabledModels, c.Pricing, c.Settings)
	if n := p.Skipped.CorruptConnections + p.Skipped.CorruptNodes + p.Skipped.CorruptKV; n > 0 {
		fmt.Fprintf(&b, "skipped %d rows that could not be read (%d connections, %d nodes, %d kv)\n", n, p.Skipped.CorruptConnections, p.Skipped.CorruptNodes, p.Skipped.CorruptKV)
	}
	if len(p.Skipped.SecuritySettings) > 0 {
		fmt.Fprintf(&b, "login policy not copied (use --with-sso): %s\n", strings.Join(p.Skipped.SecuritySettings, ", "))
	}
	if len(p.Skipped.UnknownSettings) > 0 {
		fmt.Fprintf(&b, "settings this version does not know, not copied: %s\n", strings.Join(p.Skipped.UnknownSettings, ", "))
	}
	return b.String()
}

var (
	// ErrSealedSource means the source already holds sealed secrets. The source
	// of an import must be a plain database; export a plain copy first.
	ErrSealedSource = errors.New("importer: the source holds sealed secrets; export a plain copy with db:export-plain first")
	// ErrNoConfiguration means the source has none of the configuration tables.
	ErrNoConfiguration = errors.New("importer: the source has none of the configuration tables")
)

var copiedKVScopes = []string{
	repository.ScopeModelAliases, repository.ScopeCustomModels,
	repository.ScopeDisabledModels, repository.ScopePricing,
}

// Read collects the legacy configuration from src into a Plan.
func Read(ctx context.Context, src contractsdb.DB, opt Options) (*Plan, error) {
	found := false
	for _, name := range []string{"providerConnections", "apiKeys", "combos", "settings"} {
		ok, err := repository.TableExists(ctx, src, name)
		if err != nil {
			return nil, err
		}
		found = found || ok
	}
	if !found {
		return nil, ErrNoConfiguration
	}
	p := &Plan{}
	for _, step := range []func(context.Context, contractsdb.DB, *Plan) error{
		readConnections, readKeys, readNodes, readCombos, readKV,
	} {
		if err := step(ctx, src, p); err != nil {
			return nil, err
		}
	}
	if err := readSettings(ctx, src, p, opt); err != nil {
		return nil, err
	}
	return p, nil
}

// errorState is what a new deployment must not inherit: a stale cooldown would
// keep a fresh account out of rotation.
var errorState = []string{"testStatus", "lastError", "errorCode", "lastErrorAt", "backoffLevel", "rateLimitedUntil"}

// lockPrefix marks the per-model cooldown a connection carries. It is data on
// disk, so it is spelled as stored.
const lockPrefix = "modelLock_"

// cleanConnection removes the model locks and the error state, keeping every
// other key's bytes and position.
func cleanConnection(data string) (string, error) {
	d, err := document.ParseDoc(data)
	if err != nil {
		return "", err
	}
	for _, k := range d.Keys() {
		if strings.HasPrefix(k, lockPrefix) {
			if err := d.Delete(k); err != nil {
				return "", err
			}
		}
	}
	for _, k := range errorState {
		if err := d.Delete(k); err != nil {
			return "", err
		}
	}
	return d.String(), nil
}

// holdsSealed reports whether any string leaf of a JSON document is sealed.
// Scanning the whole document rather than a field list catches a value at a
// path this version does not know.
func holdsSealed(doc string) bool {
	if !gjson.Valid(doc) {
		return false
	}
	sealed := false
	var walk func(gjson.Result)
	walk = func(r gjson.Result) {
		if sealed {
			return
		}
		switch r.Type {
		case gjson.String:
			sealed = sealer.IsSealed(r.String())
		case gjson.JSON:
			if r.IsObject() {
				r.ForEach(func(_, v gjson.Result) bool { walk(v); return !sealed })
			} else if r.IsArray() {
				r.ForEach(func(_, v gjson.Result) bool { walk(v); return !sealed })
			}
		}
	}
	walk(gjson.Parse(doc))
	return sealed
}

func readConnections(ctx context.Context, src contractsdb.DB, p *Plan) error {
	ok, err := repository.TableExists(ctx, src, "providerConnections")
	if err != nil || !ok {
		return err
	}
	var rows []struct {
		ID        string  `db:"id"`
		Provider  string  `db:"provider"`
		AuthType  string  `db:"authType"`
		Name      *string `db:"name"`
		Email     *string `db:"email"`
		Priority  *int64  `db:"priority"`
		IsActive  *int64  `db:"isActive"`
		Data      string  `db:"data"`
		CreatedAt string  `db:"createdAt"`
		UpdatedAt string  `db:"updatedAt"`
	}
	if err := src.Select(&rows, `SELECT "id","provider","authType","name","email","priority","isActive","data","createdAt","updatedAt" FROM "providerConnections" ORDER BY "provider","priority","id"`); err != nil {
		return err
	}
	for _, r := range rows {
		if holdsSealed(r.Data) {
			return ErrSealedSource
		}
		cleaned, err := cleanConnection(r.Data)
		if err != nil {
			p.Skipped.CorruptConnections++
			continue
		}
		row := connRow{
			id: r.ID, provider: r.Provider, authType: r.AuthType, data: cleaned,
			createdAt: r.CreatedAt, updatedAt: r.UpdatedAt,
			priority: r.Priority, isActive: r.IsActive, nameNull: r.Name == nil, emailNull: r.Email == nil,
		}
		if r.Name != nil {
			row.name = *r.Name
		}
		if r.Email != nil {
			row.email = *r.Email
		}
		p.connections = append(p.connections, row)
	}
	return nil
}

func readKeys(ctx context.Context, src contractsdb.DB, p *Plan) error {
	ok, err := repository.TableExists(ctx, src, "apiKeys")
	if err != nil || !ok {
		return err
	}
	restricted, allow := `NULL`, `NULL`
	for col, into := range map[string]*string{"accessRestricted": &restricted, "accessAllow": &allow} {
		has, err := repository.ColumnExists(ctx, src, "apiKeys", col)
		if err != nil {
			return err
		}
		if has {
			*into = `"` + col + `"`
		}
	}
	var rows []struct {
		ID         string  `db:"id"`
		Key        string  `db:"key"`
		Name       *string `db:"name"`
		MachineID  *string `db:"machineId"`
		IsActive   *int64  `db:"isActive"`
		CreatedAt  string  `db:"createdAt"`
		Restricted *int64  `db:"accessRestricted"`
		Allow      *string `db:"accessAllow"`
	}
	q := `SELECT "id","key","name","machineId","isActive","createdAt",` + restricted + `,` + allow + ` FROM "apiKeys" ORDER BY "createdAt","id"`
	if err := src.Select(&rows, q); err != nil {
		return err
	}
	for _, r := range rows {
		if sealer.IsSealed(r.Key) {
			return ErrSealedSource
		}
		row := keyRow{
			id: r.ID, key: r.Key, createdAt: r.CreatedAt, isActive: r.IsActive, restricted: r.Restricted,
			nameNull: r.Name == nil, machineNull: r.MachineID == nil, allowNull: r.Allow == nil,
		}
		if r.Name != nil {
			row.name = *r.Name
		}
		if r.MachineID != nil {
			row.machineID = *r.MachineID
		}
		if r.Allow != nil {
			row.allow = *r.Allow
		}
		p.apiKeys = append(p.apiKeys, row)
	}
	return nil
}

func readNodes(ctx context.Context, src contractsdb.DB, p *Plan) error {
	ok, err := repository.TableExists(ctx, src, "providerNodes")
	if err != nil || !ok {
		return err
	}
	var rows []struct {
		ID        string  `db:"id"`
		Type      *string `db:"type"`
		Name      *string `db:"name"`
		Data      string  `db:"data"`
		CreatedAt string  `db:"createdAt"`
		UpdatedAt string  `db:"updatedAt"`
	}
	if err := src.Select(&rows, `SELECT "id","type","name","data","createdAt","updatedAt" FROM "providerNodes" ORDER BY "createdAt","id"`); err != nil {
		return err
	}
	for _, r := range rows {
		if _, err := document.ParseDoc(r.Data); err != nil {
			p.Skipped.CorruptNodes++
			continue
		}
		row := nodeRow{id: r.ID, data: r.Data, createdAt: r.CreatedAt, updatedAt: r.UpdatedAt, typNull: r.Type == nil, nameNull: r.Name == nil}
		if r.Type != nil {
			row.typ = *r.Type
		}
		if r.Name != nil {
			row.name = *r.Name
		}
		p.nodes = append(p.nodes, row)
	}
	return nil
}

func readCombos(ctx context.Context, src contractsdb.DB, p *Plan) error {
	ok, err := repository.TableExists(ctx, src, "combos")
	if err != nil || !ok {
		return err
	}
	var rows []struct {
		ID        string  `db:"id"`
		Name      string  `db:"name"`
		Kind      *string `db:"kind"`
		Models    string  `db:"models"`
		CreatedAt string  `db:"createdAt"`
		UpdatedAt string  `db:"updatedAt"`
	}
	if err := src.Select(&rows, `SELECT "id","name","kind","models","createdAt","updatedAt" FROM "combos" ORDER BY "createdAt","id"`); err != nil {
		return err
	}
	for _, r := range rows {
		row := comboRow{id: r.ID, name: r.Name, models: r.Models, createdAt: r.CreatedAt, updatedAt: r.UpdatedAt, kindNull: r.Kind == nil}
		if r.Kind != nil {
			row.kind = *r.Kind
		}
		p.combos = append(p.combos, row)
	}
	return nil
}

func readKV(ctx context.Context, src contractsdb.DB, p *Plan) error {
	ok, err := repository.TableExists(ctx, src, "kv")
	if err != nil || !ok {
		return err
	}
	for _, scope := range copiedKVScopes {
		var rows []struct {
			Key   string `db:"key"`
			Value string `db:"value"`
		}
		if err := src.Select(&rows, `SELECT "key","value" FROM "kv" WHERE "scope" = ? ORDER BY "key"`, scope); err != nil {
			return err
		}
		for _, r := range rows {
			if !gjson.Valid(r.Value) {
				p.Skipped.CorruptKV++
				continue
			}
			p.kv = append(p.kv, kvRow{scope: scope, key: r.Key, value: r.Value})
		}
	}
	return nil
}

func readSettings(ctx context.Context, src contractsdb.DB, p *Plan, opt Options) error {
	ok, err := repository.TableExists(ctx, src, "settings")
	if err != nil || !ok {
		return err
	}
	var raw []string
	if err := src.Select(&raw, `SELECT "data" FROM "settings" WHERE "id" = 1`); err != nil {
		return err
	}
	if len(raw) == 0 {
		return nil
	}
	if holdsSealed(raw[0]) {
		return ErrSealedSource
	}
	doc, err := document.ParseDoc(raw[0])
	if err != nil {
		return fmt.Errorf("importer: the source settings row is not valid JSON: %w", err)
	}
	for _, k := range doc.Keys() {
		v, _ := doc.GetRaw(k)
		switch classifySetting(k) {
		case classCopy:
			p.settings = append(p.settings, settingRow{k, v})
		case classSecurity:
			if opt.WithSSO {
				p.settings = append(p.settings, settingRow{k, v})
			} else {
				p.Skipped.SecuritySettings = append(p.Skipped.SecuritySettings, k)
			}
		case classUnknown:
			p.Skipped.UnknownSettings = append(p.Skipped.UnknownSettings, k)
		}
	}
	sort.Strings(p.Skipped.UnknownSettings)
	sort.Strings(p.Skipped.SecuritySettings)
	return nil
}
