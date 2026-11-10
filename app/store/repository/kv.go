package repository

import (
	"context"
	"fmt"
	"strconv"

	"github.com/tidwall/gjson"

	contractsdb "github.com/goravel/framework/contracts/database/db"
	"rllmx/app/store/dialect"
	"rllmx/app/store/document"
)

func (s *Store) kvGet(x contractsdb.Tx, scope, key string) (string, bool, error) {
	var v []string
	if err := s.sel(x, &v, `SELECT "value" FROM "kv" WHERE "scope" = ? AND "key" = ?`, dialect.StripNUL(scope), dialect.StripNUL(key)); err != nil {
		return "", false, err
	}
	if len(v) == 0 {
		return "", false, nil
	}
	return v[0], true, nil
}

func (s *Store) kvPut(x contractsdb.Tx, scope, key, value string) error {
	return s.stmt(x, `INSERT INTO "kv"("scope","key","value") VALUES(?,?,?) ON CONFLICT("scope","key") DO UPDATE SET "value"=excluded."value"`,
		dialect.StripNUL(scope), dialect.StripNUL(key), dialect.StripNUL(value))
}

// KV scope names shared with the configuration database.
const (
	ScopeModelAliases   = "modelAliases"
	ScopeDisabledModels = "disabledModels"
	ScopeCustomModels   = "customModels"
	ScopePricing        = "pricing"
)

// ScopeResponseCache is a Go-only scope: key = API key id, value
// {"enabled":true} when that key may use the response cache. A key with no row
// is off. The 9router schema ignores it.
const ScopeResponseCache = "rllmResponseCache"

// KVRepo reads and writes the kv table.
type KVRepo struct{ s *Store }

// KV returns the key-value repository.
func (s *Store) KV() *KVRepo { return &KVRepo{s} }

type kvRow struct {
	Key   string `db:"key"`
	Value string `db:"value"`
}

// Get returns the stored text; ok is false for a key that was never set.
func (r *KVRepo) Get(ctx context.Context, scope, key string) (string, bool, error) {
	return r.s.kvGet(r.s.on(ctx), scope, key)
}

// List returns every value of a scope.
func (r *KVRepo) List(ctx context.Context, scope string) (map[string]string, error) {
	var rows []kvRow
	if err := r.s.sel(r.s.on(ctx), &rows, `SELECT "key" AS key, "value" AS value FROM "kv" WHERE "scope" = ?`, dialect.StripNUL(scope)); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, row := range rows {
		out[row.Key] = row.Value
	}
	return out, nil
}

// Set stores a JSON value.
func (r *KVRepo) Set(ctx context.Context, scope, key, value string) error {
	if !gjson.Valid(value) {
		return fmt.Errorf("repository: kv value for %s/%s is not valid JSON", scope, key)
	}
	return r.s.kvPut(r.s.on(ctx), scope, key, value)
}

// Increment adds one to a counter value in one transaction and returns the new
// value. A missing or non-numeric value counts as zero.
func (r *KVRepo) Increment(ctx context.Context, scope, key string) (int64, error) {
	var next int64
	err := r.s.Tx(ctx, func(tx contractsdb.Tx) error {
		v, _, err := r.s.kvGet(tx, scope, key)
		if err != nil {
			return err
		}
		cur, _ := strconv.ParseInt(v, 10, 64)
		next = cur + 1
		return r.s.kvPut(tx, scope, key, strconv.FormatInt(next, 10))
	})
	return next, err
}

func decodeStrings(raw string) []string {
	out := []string{}
	for _, v := range gjson.Parse(raw).Array() {
		if v.Type == gjson.String {
			out = append(out, v.String())
		}
	}
	return out
}

func encodeStrings(list []string) string {
	b := []byte("[")
	for i, v := range list {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, document.Encode(dialect.StripNUL(v))...)
	}
	return string(append(b, ']'))
}

// DisabledModels lists the models switched off for a provider.
func (r *KVRepo) DisabledModels(ctx context.Context, provider string) ([]string, error) {
	raw, ok, err := r.Get(ctx, ScopeDisabledModels, provider)
	if err != nil || !ok {
		return []string{}, err
	}
	return decodeStrings(raw), nil
}

// ResponseCacheEnabled reports whether the API key may use the response cache.
// A missing row, a value that is not an object and anything but a JSON true are
// off, so a bad row can only switch the feature off.
func (r *KVRepo) ResponseCacheEnabled(ctx context.Context, keyID string) (bool, error) {
	raw, ok, err := r.Get(ctx, ScopeResponseCache, keyID)
	if err != nil || !ok {
		return false, err
	}
	v := gjson.Get(raw, "enabled")
	return v.Type == gjson.True, nil
}
