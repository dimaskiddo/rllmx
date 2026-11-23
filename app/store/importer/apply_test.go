package importer

import (
	"context"
	"errors"
	"rllmx/app/store/dialect"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"rllmx/app/store/repository"
	"rllmx/app/store/sealer"
	"rllmx/tests/store"
)

func apply(t testing.TB, dest *store.Store, opt Options) Counts {
	t.Helper()
	p := read(t, opt)
	got, err := Apply(context.Background(), dest.Store, p)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	return got
}

func TestApplyCopiesEverythingIntoAnEmptyDatabase(t *testing.T) {
	dest := emptyDest(t)
	p := read(t, Options{})
	got, err := Apply(context.Background(), dest.Store, p)
	if err != nil {
		t.Fatal(err)
	}
	if got != p.Counts() {
		t.Fatalf("wrote %+v, planned %+v", got, p.Counts())
	}
	var n []int64
	for table, want := range map[string]int64{
		"providerConnections": 2, "apiKeys": 2, "providerNodes": 1, "combos": 1,
	} {
		if err := dest.SQ.DB.Select(&n, `SELECT count(*) FROM "`+table+`"`); err != nil {
			t.Fatal(err)
		}
		if len(n) == 0 || n[0] != want {
			t.Errorf("%s holds %v rows, want %d", table, n, want)
		}
	}
	var meta []string
	if err := dest.SQ.DB.Select(&meta, `SELECT "value" FROM "_meta" WHERE "key" = 'totalRequestsLifetime'`); err != nil {
		t.Fatal(err)
	}
	if len(meta) != 0 {
		t.Fatalf("the lifetime counter travelled: %v", meta)
	}
}

func TestApplyRefusesADestinationWithoutTheSchema(t *testing.T) {
	dest := store.New(t)
	_, err := Apply(context.Background(), dest.Store, read(t, Options{}))
	if !errors.Is(err, repository.ErrNeedsMigration) {
		t.Fatalf("err = %v", err)
	}
}

func TestApplyKeepsEveryRowByteForByte(t *testing.T) {
	dest := emptyDest(t)
	apply(t, dest, Options{})
	var rows []struct {
		ID        string `db:"id"`
		Name      string `db:"name"`
		CreatedAt string `db:"createdAt"`
	}
	if err := dest.SQ.DB.Select(&rows, `SELECT "id","name","createdAt" FROM "combos"`); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != "combo-1" || rows[0].Name != "fast" || rows[0].CreatedAt != "2026-01-01T00:00:00.000Z" {
		t.Fatalf("combo = %+v", rows)
	}
	var kv []struct {
		Scope string `db:"scope"`
		Key   string `db:"key"`
		Value string `db:"value"`
	}
	if err := dest.SQ.DB.Select(&kv, `SELECT "scope","key","value" FROM "kv" WHERE "scope" <> 'rllmCrypto' ORDER BY "scope"`); err != nil {
		t.Fatal(err)
	}
	want := []struct{ scope, value string }{
		{"customModels", `{"providerAlias":"oa","id":"x","type":"llm"}`},
		{"disabledModels", `["gpt-4"]`},
		{"modelAliases", `"openai/gpt-5"`},
		{"pricing", `{"gpt-5":{"input":1}}`},
	}
	if len(kv) != len(want) {
		t.Fatalf("%d kv rows", len(kv))
	}
	for i, w := range want {
		if kv[i].Scope != w.scope || kv[i].Value != w.value {
			t.Errorf("kv[%d] = %+v, want %s %s", i, kv[i], w.scope, w.value)
		}
	}
}

func TestApplyWritesSecretsSealed(t *testing.T) {
	dest := emptyDest(t)
	apply(t, dest, Options{})
	var keys []string
	if err := dest.SQ.DB.Select(&keys, `SELECT "key" FROM "apiKeys" ORDER BY "id"`); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || !sealer.IsSealed(keys[0]) || !sealer.IsSealed(keys[1]) {
		t.Fatalf("keys = %v", keys)
	}
	if keys[0] == keys[1] {
		t.Fatal("two different keys sealed to the same text")
	}
	if plain, err := dest.OpenAPIKey(keys[0]); err != nil || plain != "sk-alpha-111111-aaaa" {
		t.Fatalf("open = %q %v", plain, err)
	}
	var conns []struct {
		ID   string `db:"id"`
		Data string `db:"data"`
	}
	if err := dest.SQ.DB.Select(&conns, `SELECT "id","data" FROM "providerConnections" ORDER BY "id"`); err != nil {
		t.Fatal(err)
	}
	for _, c := range conns {
		opened, ok := dest.OpenConnectionData(c.ID, c.Data)
		if !ok {
			t.Fatalf("%s does not open", c.ID)
		}
		if strings.Contains(opened, "enc:v1:") {
			t.Fatalf("%s still holds sealed text after opening", c.ID)
		}
	}
	if !strings.Contains(conns[0].Data, "enc:v1:") {
		t.Fatalf("the connection was stored plain: %s", conns[0].Data)
	}
}

// The one secret a settings row can hold is the SSO client secret, so it is
// sealed only when the policy travels.
func TestApplySealsTheSSOClientSecret(t *testing.T) {
	dest := emptyDest(t)
	apply(t, dest, Options{WithSSO: true})
	var raw []string
	if err := dest.SQ.DB.Select(&raw, `SELECT "data" FROM "settings" WHERE "id" = 1`); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw[0], "enc:v1:") {
		t.Fatalf("the settings row was stored plain: %s", raw[0])
	}
	d, err := dest.Settings().Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := d.GetString("oidcClientSecret"); v != "sec" {
		t.Fatalf("the client secret does not open: %q", v)
	}
	if v, _ := d.GetString("authMode"); v != "oidc" {
		t.Fatalf("authMode = %q", v)
	}
}

func TestApplyKeepsTheDestinationPassword(t *testing.T) {
	dest := emptyDest(t)
	if err := dest.Settings().SetPasswordHash(context.Background(), "$2a$10$keepme"); err != nil {
		t.Fatal(err)
	}
	apply(t, dest, Options{})
	d, err := dest.Settings().Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := d.GetString("password"); v != "$2a$10$keepme" {
		t.Fatalf("the destination password changed: %q", v)
	}
	if v, _ := d.GetInt("stickyRoundRobinLimit"); v != 5 {
		t.Fatalf("the source setting did not arrive: %d", v)
	}
	if v, _ := d.GetString("authMode"); v != "" {
		t.Fatalf("the login policy arrived without --with-sso: %q", v)
	}
}

func TestApplyIsAtomic(t *testing.T) {
	dest := emptyDest(t)
	p := read(t, Options{})
	// A key that collides with one already in the plan makes the second insert
	// fail after the first rows were written.
	p.apiKeys = append(p.apiKeys, p.apiKeys[0])
	if _, err := Apply(context.Background(), dest.Store, p); err == nil {
		t.Fatal("a duplicate key was accepted")
	}
	var n []int64
	for _, table := range []string{"providerConnections", "apiKeys", "providerNodes", "combos"} {
		if err := dest.SQ.DB.Select(&n, `SELECT count(*) FROM "`+table+`"`); err != nil {
			t.Fatal(err)
		}
		if n[0] != 0 {
			t.Fatalf("%s kept %d rows from a failed apply", table, n[0])
		}
	}
}

func TestSecondApplyIsRefused(t *testing.T) {
	dest := emptyDest(t)
	apply(t, dest, Options{})
	p := read(t, Options{})
	if _, err := Apply(context.Background(), dest.Store, p); !errors.Is(err, ErrDestinationNotEmpty) {
		t.Fatalf("err = %v", err)
	}
}

func TestDestinationMustBeEmpty(t *testing.T) {
	for name, fill := range map[string]string{
		"connection": `INSERT INTO "providerConnections"("id","provider","authType","data","createdAt","updatedAt") VALUES('x','p','apikey','{}','t','t')`,
		"api key":    `INSERT INTO "apiKeys"("id","key","createdAt") VALUES('x','sk-x','t')`,
		"node":       `INSERT INTO "providerNodes"("id","data","createdAt","updatedAt") VALUES('x','{}','t','t')`,
		"combo":      `INSERT INTO "combos"("id","name","models","createdAt","updatedAt") VALUES('x','n','[]','t','t')`,
		"kv":         `INSERT INTO "kv"("scope","key","value") VALUES('modelAliases','a','"b"')`,
		"setting":    `INSERT INTO "settings"("id","data") VALUES(1,'{"requireLogin":false}')`,
	} {
		t.Run(name, func(t *testing.T) {
			dest := emptyDest(t)
			if err := dest.SQ.DB.Statement(fill); err != nil {
				t.Fatal(err)
			}
			if _, err := Apply(context.Background(), dest.Store, read(t, Options{})); !errors.Is(err, ErrDestinationNotEmpty) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestAPasswordHashDoesNotMakeTheDestinationNonEmpty(t *testing.T) {
	dest := emptyDest(t)
	if err := dest.Settings().SetPasswordHash(context.Background(), "$2a$10$hash"); err != nil {
		t.Fatal(err)
	}
	apply(t, dest, Options{})
}

// A destination protected by a different secret must fail cleanly and leave
// nothing behind, rather than writing rows that can never be opened.
func TestApplyWithoutASecretWritesNothing(t *testing.T) {
	dest := store.Migrated(t)
	bare, err := repository.New(repository.Options{
		DB: dest.SQ.DB, Schema: dest.SQ.Schema, Log: dest.SQ.Log, Dialect: dialect.SQLite,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(context.Background(), bare, read(t, Options{})); err == nil || !strings.Contains(err.Error(), "DB_ENCRYPTION_SECRET") {
		t.Fatalf("err = %v", err)
	}
	var n []int64
	if err := dest.SQ.DB.Select(&n, `SELECT count(*) FROM "providerConnections"`); err != nil {
		t.Fatal(err)
	}
	if n[0] != 0 {
		t.Fatalf("%d rows were left behind", n[0])
	}
}

func TestTheCleanedConnectionIsWhatGetsStored(t *testing.T) {
	dest := emptyDest(t)
	apply(t, dest, Options{})
	var conns []struct {
		ID   string `db:"id"`
		Data string `db:"data"`
	}
	if err := dest.SQ.DB.Select(&conns, `SELECT "id","data" FROM "providerConnections" ORDER BY "id"`); err != nil {
		t.Fatal(err)
	}
	opened, ok := dest.OpenConnectionData(conns[0].ID, conns[0].Data)
	if !ok {
		t.Fatal("does not open")
	}
	if gjson.Get(opened, "testStatus").Exists() || gjson.Get(opened, "modelLock_gpt-5").Exists() {
		t.Fatalf("the stored row still holds a cooldown: %s", opened)
	}
	if gjson.Get(opened, "apiKey").String() == "" {
		t.Fatalf("the credential was lost: %s", opened)
	}
}
