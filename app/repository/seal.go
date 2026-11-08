package repository

import (
	"context"
	"fmt"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	contractsdb "github.com/goravel/framework/contracts/database/db"

	"rllmx/app/dialect"
	"rllmx/app/document"
	"rllmx/app/sealer"
)

// sealPlan is what a scan found: plain secret values still to be sealed, and
// sealed values that do not open under the configured secret.
type sealPlan struct {
	keys, connFields, settingFields int
	unopenable                      int
	skipped                         int
}

func (p sealPlan) values() int { return p.keys + p.connFields + p.settingFields }

// sealDoc seals every plain secret string at paths and counts the sealed
// values that do not open. A value that is already sealed is left as it is.
func sealDoc(box *sealer.Box, doc string, paths []string, aad func(string) string) (out string, plain, bad int, err error) {
	out = doc
	for _, p := range paths {
		r := gjson.Get(out, p)
		if r.Type != gjson.String || r.String() == "" {
			continue
		}
		if sealer.IsSealed(r.String()) {
			if _, oerr := box.Open(aad(p), r.String()); oerr != nil {
				bad++
			}
			continue
		}
		next, serr := sjson.SetRaw(out, p, document.Encode(box.Seal(aad(p), r.String())))
		if serr != nil {
			return doc, 0, 0, fmt.Errorf("repository: seal %s: %w", p, serr)
		}
		out = next
		plain++
	}
	return out, plain, bad, nil
}

type idValue struct {
	ID string `db:"id"`
	V  string `db:"v"`
}

// readPairs returns the rows of a query that selects "id" and "v". A table the
// database does not have yields no rows.
func (s *Store) readPairs(x contractsdb.Tx, table, query string) ([]idValue, error) {
	if !s.hasTable(table) {
		return nil, nil
	}
	var out []idValue
	if err := s.sel(x, &out, query); err != nil {
		return nil, err
	}
	return out, nil
}

const (
	selectKeys     = `SELECT "id" AS id, COALESCE("key", '') AS v FROM "apiKeys"`
	selectConns    = `SELECT "id" AS id, "data" AS v FROM "providerConnections"`
	selectSettings = `SELECT CAST('settings' AS TEXT) AS id, "data" AS v FROM "settings" WHERE "id" = 1`
)

// scanSecrets walks every table that holds secrets. With apply it also writes
// the sealed values, touching only rows that had a plain secret. A row whose
// JSON does not parse is counted and left alone.
func (s *Store) scanSecrets(x contractsdb.Tx, apply bool) (sealPlan, error) {
	var p sealPlan
	box, err := s.sealer()
	if err != nil {
		return p, err
	}

	keys, err := s.readPairs(x, "apiKeys", selectKeys)
	if err != nil {
		return p, err
	}
	for _, k := range keys {
		switch {
		case k.V == "":
		case sealer.IsSealed(k.V):
			if _, err := box.Open(apiKeyAAD, k.V); err != nil {
				p.unopenable++
			}
		default:
			p.keys++
			if apply {
				sealed, err := s.SealAPIKey(k.V)
				if err != nil {
					return p, err
				}
				if err := s.stmt(x, `UPDATE "apiKeys" SET "key"=? WHERE "id"=?`, sealed, k.ID); err != nil {
					return p, err
				}
			}
		}
	}

	conns, err := s.readPairs(x, "providerConnections", selectConns)
	if err != nil {
		return p, err
	}
	for _, c := range conns {
		if _, perr := document.ParseDoc(c.V); perr != nil {
			p.skipped++
			continue
		}
		out, plain, bad, err := sealDoc(box, c.V, connSecretPaths, connAAD(c.ID))
		if err != nil {
			return p, err
		}
		p.connFields += plain
		p.unopenable += bad
		if apply && plain > 0 {
			if err := s.stmt(x, `UPDATE "providerConnections" SET "data"=? WHERE "id"=?`, dialect.StripNUL(out), c.ID); err != nil {
				return p, err
			}
		}
	}

	settings, err := s.readPairs(x, "settings", selectSettings)
	if err != nil {
		return p, err
	}
	for _, st := range settings {
		if _, perr := document.ParseDoc(st.V); perr != nil {
			p.skipped++
			continue
		}
		out, plain, bad, err := sealDoc(box, st.V, settingsSecretPaths, settingsAAD)
		if err != nil {
			return p, err
		}
		p.settingFields += plain
		p.unopenable += bad
		if apply && plain > 0 {
			if err := s.stmt(x, `UPDATE "settings" SET "data"=? WHERE "id"=1`, dialect.StripNUL(out)); err != nil {
				return p, err
			}
		}
	}
	return p, nil
}

// checkState reports whether the database carries the check value, and fails
// with ErrWrongEncryptionSecret when it does not open under this secret.
func (s *Store) checkState(x contractsdb.Tx) (bool, error) {
	box, err := s.sealer()
	if err != nil {
		return false, err
	}
	if !s.hasTable("kv") {
		return false, nil
	}
	v, found, err := s.kvGet(x, cryptScope, cryptCheckKey)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}
	if _, err := box.Open(cryptCheckAAD, gjson.Parse(v).String()); err != nil {
		return true, ErrWrongEncryptionSecret
	}
	return true, nil
}

func (s *Store) writeCheck(x contractsdb.Tx) error {
	box, err := s.sealer()
	if err != nil {
		return err
	}
	return s.kvPut(x, cryptScope, cryptCheckKey, document.Encode(box.Seal(cryptCheckAAD, cryptCheckVal)))
}

// Verify is the read-only guard for commands that must not convert. A wrong
// secret is refused, and so is a database that is missing pieces or still
// holds plain secrets: the server has to open that one first. It writes
// nothing.
func (s *Store) Verify(ctx context.Context) error {
	if _, err := s.sealer(); err != nil {
		return err
	}
	p, err := s.computePlan(ctx)
	if err != nil {
		return err
	}
	if !p.empty() {
		return ErrNeedsMigration
	}
	present, err := s.checkState(s.on(ctx))
	if err != nil {
		return err
	}
	if present {
		return nil
	}
	sp, err := s.scanSecrets(s.on(ctx), false)
	if err != nil {
		return err
	}
	if sp.unopenable > 0 {
		return ErrWrongEncryptionSecret
	}
	if sp.values() > 0 {
		return ErrNeedsMigration
	}
	return nil
}

// legacyUsageKeys counts usage rows of the previous program that still hold a
// client key in plain text. The table is never written here.
func (s *Store) legacyUsageKeys(ctx context.Context) int {
	if !s.hasTable("usageHistory") {
		return 0
	}
	n, err := s.count(s.on(ctx), `SELECT count(*) FROM "usageHistory" WHERE "apiKey" IS NOT NULL AND "apiKey" <> ''`)
	if err != nil {
		return 0
	}
	return int(n)
}

// unsealAll writes the plain value of every sealed secret; plain values stay as they are.
func (s *Store) unsealAll(x contractsdb.Tx) error {
	box, err := s.sealer()
	if err != nil {
		return err
	}
	keys, err := s.readPairs(x, "apiKeys", selectKeys)
	if err != nil {
		return err
	}
	for _, k := range keys {
		plain, err := s.OpenAPIKey(k.V)
		if err != nil {
			return fmt.Errorf("repository: a client key does not open: %w", ErrWrongEncryptionSecret)
		}
		if plain == k.V {
			continue
		}
		if err := s.stmt(x, `UPDATE "apiKeys" SET "key"=? WHERE "id"=?`, plain, k.ID); err != nil {
			return err
		}
	}
	conns, err := s.readPairs(x, "providerConnections", selectConns)
	if err != nil {
		return err
	}
	for _, c := range conns {
		out, ok := openFields(box, c.V, connSecretPaths, connAAD(c.ID))
		if !ok {
			return fmt.Errorf("repository: connection data does not open: %w", ErrWrongEncryptionSecret)
		}
		if out == c.V {
			continue
		}
		if err := s.stmt(x, `UPDATE "providerConnections" SET "data"=? WHERE "id"=?`, out, c.ID); err != nil {
			return err
		}
	}
	settings, err := s.readPairs(x, "settings", selectSettings)
	if err != nil {
		return err
	}
	for _, st := range settings {
		out, ok := openFields(box, st.V, settingsSecretPaths, settingsAAD)
		if !ok {
			return fmt.Errorf("repository: settings do not open: %w", ErrWrongEncryptionSecret)
		}
		if out == st.V {
			continue
		}
		if err := s.stmt(x, `UPDATE "settings" SET "data"=? WHERE "id"=1`, out); err != nil {
			return err
		}
	}
	return nil
}
