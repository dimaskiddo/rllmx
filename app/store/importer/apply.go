package importer

import (
	"context"
	"errors"
	"fmt"
	"rllmx/app/store/dialect"
	"rllmx/app/store/document"

	contractsdb "github.com/goravel/framework/contracts/database/db"

	"rllmx/app/store/repository"
)

// ErrDestinationNotEmpty means the target already holds configuration.
var ErrDestinationNotEmpty = errors.New("the destination database is not empty; import-config only seeds an empty one")

// RequireSchema refuses a destination the server has not opened yet. The
// command never runs a migration, and writing into a missing table would answer
// with the driver's error instead of the one that says what to do.
func RequireSchema(ctx context.Context, dest *repository.Store) error {
	missing, err := dest.NeedsMigration(ctx)
	if err != nil {
		return err
	}
	if missing {
		return repository.ErrNeedsMigration
	}
	return nil
}

// Apply writes the plan into dest in one transaction. The destination needs the
// schema the server creates and no configuration rows (a dashboard password
// hash is the one thing allowed). It returns the counts written.
func Apply(ctx context.Context, dest *repository.Store, p *Plan) (Counts, error) {
	if err := RequireSchema(ctx, dest); err != nil {
		return Counts{}, err
	}
	var got Counts
	err := dest.Tx(ctx, func(tx contractsdb.Tx) error {
		got = Counts{}
		populated, err := dest.HasConfigurationTx(tx)
		if err != nil {
			return err
		}
		if populated {
			return ErrDestinationNotEmpty
		}
		return write(ctx, dest, tx, p, &got)
	})
	if err != nil {
		return Counts{}, err
	}
	return got, nil
}

func intOrNil(n *int64) any {
	if n == nil {
		return nil
	}
	return *n
}

func textOrNil(s string, isNull bool) any {
	if isNull {
		return nil
	}
	return dialect.StripNUL(s)
}

// write inserts the plan with the original ids, timestamps and column values,
// so every copied row matches its source apart from the cleaned connection data
// and the sealing.
func write(ctx context.Context, dest *repository.Store, tx contractsdb.Tx, p *Plan, got *Counts) error {
	d := dest.Dialect()
	stmt := func(q string, args ...any) error {
		return tx.Statement(dialect.Rebind(q, d), args...)
	}
	for _, r := range p.connections {
		data, err := dest.SealConnectionData(dialect.StripNUL(r.id), dialect.StripNUL(r.data))
		if err != nil {
			return err
		}
		if err := stmt(`INSERT INTO "providerConnections"("id","provider","authType","name","email","priority","isActive","data","createdAt","updatedAt") VALUES(?,?,?,?,?,?,?,?,?,?)`,
			dialect.StripNUL(r.id), dialect.StripNUL(r.provider), dialect.StripNUL(r.authType),
			textOrNil(r.name, r.nameNull), textOrNil(r.email, r.emailNull), intOrNil(r.priority), activeOrDefault(r.isActive),
			data, dialect.StripNUL(r.createdAt), dialect.StripNUL(r.updatedAt)); err != nil {
			return err
		}
		got.Connections++
	}
	for _, r := range p.apiKeys {
		restricted := int64(0)
		if r.restricted != nil && *r.restricted == 1 {
			restricted = 1
		}
		allow := "[]"
		if !r.allowNull && r.allow != "" {
			allow = r.allow
		}
		sealed, err := dest.SealAPIKey(r.key)
		if err != nil {
			return err
		}
		if err := stmt(`INSERT INTO "apiKeys"("id","key","name","machineId","isActive","createdAt","accessRestricted","accessAllow") VALUES(?,?,?,?,?,?,?,?)`,
			dialect.StripNUL(r.id), sealed, textOrNil(r.name, r.nameNull), textOrNil(r.machineID, r.machineNull),
			activeOrDefault(r.isActive), dialect.StripNUL(r.createdAt), restricted, dialect.StripNUL(allow)); err != nil {
			return err
		}
		got.APIKeys++
	}
	for _, r := range p.nodes {
		if err := stmt(`INSERT INTO "providerNodes"("id","type","name","data","createdAt","updatedAt") VALUES(?,?,?,?,?,?)`,
			dialect.StripNUL(r.id), textOrNil(r.typ, r.typNull), textOrNil(r.name, r.nameNull),
			dialect.StripNUL(r.data), dialect.StripNUL(r.createdAt), dialect.StripNUL(r.updatedAt)); err != nil {
			return err
		}
		got.Nodes++
	}
	for _, r := range p.combos {
		if err := stmt(`INSERT INTO "combos"("id","name","kind","models","createdAt","updatedAt") VALUES(?,?,?,?,?,?)`,
			dialect.StripNUL(r.id), dialect.StripNUL(r.name), textOrNil(r.kind, r.kindNull),
			dialect.StripNUL(r.models), dialect.StripNUL(r.createdAt), dialect.StripNUL(r.updatedAt)); err != nil {
			return err
		}
		got.Combos++
	}
	for _, r := range p.kv {
		if err := stmt(`INSERT INTO "kv"("scope","key","value") VALUES(?,?,?)`,
			dialect.StripNUL(r.scope), dialect.StripNUL(r.key), dialect.StripNUL(r.value)); err != nil {
			return err
		}
		switch r.scope {
		case repository.ScopeModelAliases:
			got.ModelAliases++
		case repository.ScopeCustomModels:
			got.CustomModels++
		case repository.ScopeDisabledModels:
			got.DisabledModels++
		case repository.ScopePricing:
			got.Pricing++
		}
	}
	got.Settings += len(p.settings)
	return writeSettings(dest, tx, p)
}

func activeOrDefault(n *int64) int64 {
	if n != nil {
		return *n
	}
	return 1
}

// writeSettings merges the planned keys into the destination's settings row and
// seals it. The row is the one a fresh destination already has, so the copy
// keeps everything the destination set for itself.
func writeSettings(dest *repository.Store, tx contractsdb.Tx, p *Plan) error {
	if len(p.settings) == 0 {
		return nil
	}
	var raw []string
	if err := tx.Select(&raw, dialect.Rebind(`SELECT "data" FROM "settings" WHERE "id" = 1`, dest.Dialect())); err != nil {
		return err
	}
	text := "{}"
	if len(raw) > 0 {
		text = raw[0]
	}
	d, err := document.ParseDoc(text)
	if err != nil {
		return fmt.Errorf("importer: the destination settings row is not valid JSON: %w", err)
	}
	for _, s := range p.settings {
		if err := d.SetRaw(s.key, s.raw); err != nil {
			return err
		}
	}
	sealed, err := dest.SealSettings(dialect.StripNUL(d.String()))
	if err != nil {
		return err
	}
	return tx.Statement(dialect.Rebind(`INSERT INTO "settings"("id","data") VALUES(1,?) ON CONFLICT("id") DO UPDATE SET "data"=excluded."data"`, dest.Dialect()), sealed)
}
