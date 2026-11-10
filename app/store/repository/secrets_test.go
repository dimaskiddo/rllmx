package repository

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"rllmx/app/store/sealer"
	"rllmx/tests"
)

// The expected value comes from an independent implementation, computed over
// the secret bytes 0x00..0x1f that tests.DBSecret spells out. It pins the
// additional data of the key column, which is part of the stored format: every
// database sealed so far opens only under this exact label.
func TestAPIKeyColumnMatchesTheIndependentVector(t *testing.T) {
	s := newMigrated(t)
	got, err := s.SealAPIKey("sk-test-key")
	if err != nil {
		t.Fatal(err)
	}
	const want = "enc:v1:1ZeWayEIxDOemS7W1HfMeVJBQxKwrXcs1tFaoLw_pf9M6pEI4M5S"
	if got != want {
		t.Fatalf("SealAPIKey = %s, want %s", got, want)
	}
	plain, err := s.OpenAPIKey(want)
	if err != nil || plain != "sk-test-key" {
		t.Fatalf("OpenAPIKey = %q, %v", plain, err)
	}
}

func TestStoredLabelsAreTheOnesTheFormatNames(t *testing.T) {
	s := newMigrated(t)
	box := tests.Box(t, tests.DBSecret)

	conn, err := s.SealConnectionData("c1", `{"apiKey":"sk-p","providerSpecificData":{"cookie":"ck"}}`)
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{"apiKey": "sk-p", "providerSpecificData.cookie": "ck"} {
		got, err := box.Open("rllm/v1/providerConnections/c1/"+path, gjson.Get(conn, path).String())
		if err != nil || got != want {
			t.Errorf("%s does not open under its stored label: %q, %v", path, got, err)
		}
	}

	set, err := s.SealSettings(`{"oidcClientSecret":"idp-secret","oidcClientId":"cid"}`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := box.Open("rllm/v1/settings/oidcClientSecret", gjson.Get(set, "oidcClientSecret").String())
	if err != nil || got != "idp-secret" {
		t.Fatalf("settings secret: %q, %v", got, err)
	}
	if gjson.Get(set, "oidcClientId").String() != "cid" {
		t.Fatalf("a non-secret setting changed: %s", set)
	}

	check, found, err := s.kvGet(s.SQ.DB, cryptScope, cryptCheckKey)
	if err != nil || !found {
		t.Fatalf("check row: %v %v", found, err)
	}
	if v, err := box.Open("rllm/v1/check", gjson.Parse(check).String()); err != nil || v != "rllm-crypto-check" {
		t.Fatalf("check row: %q, %v", v, err)
	}
	if cryptScope != "rllmCrypto" || cryptCheckKey != "check" {
		t.Fatalf("the check row moved to %s/%s", cryptScope, cryptCheckKey)
	}
}

func TestConnectionSecretMovedToAnotherRowDoesNotOpen(t *testing.T) {
	s := newMigrated(t)
	sealed, err := s.SealConnectionData("a", `{"apiKey":"sk-a"}`)
	if err != nil {
		t.Fatal(err)
	}
	if out, ok := s.OpenConnectionData("a", sealed); !ok || gjson.Get(out, "apiKey").String() != "sk-a" {
		t.Fatalf("own row: %q %v", out, ok)
	}
	if _, ok := s.OpenConnectionData("b", sealed); ok {
		t.Fatal("a secret copied to another row opened")
	}
}

func TestSealingKeepsTheStoredCiphertextOfAnUnchangedSecret(t *testing.T) {
	s := newMigrated(t)
	box := tests.Box(t, tests.DBSecret)
	first, err := sealFields(box, `{"apiKey":"sk-p"}`, "", connSecretPaths, connAAD("c"))
	if err != nil {
		t.Fatal(err)
	}
	again, err := sealFields(box, `{"apiKey":"sk-p"}`, first, connSecretPaths, connAAD("c"))
	if err != nil {
		t.Fatal(err)
	}
	if gjson.Get(first, "apiKey").String() != gjson.Get(again, "apiKey").String() {
		t.Fatal("an unchanged secret was sealed again")
	}
	changed, err := sealFields(box, `{"apiKey":"sk-new"}`, first, connSecretPaths, connAAD("c"))
	if err != nil {
		t.Fatal(err)
	}
	if gjson.Get(changed, "apiKey").String() == gjson.Get(first, "apiKey").String() {
		t.Fatal("a changed secret kept the old ciphertext")
	}
	if strings.Contains(changed, "sk-new") {
		t.Fatal("a changed secret was stored in the clear")
	}
	_ = s
}

func TestSecretsNeedASecret(t *testing.T) {
	file := makeLegacy(t)
	s := reopen(t, file, "")
	if _, err := s.SealAPIKey("sk-x"); err != ErrEncryptionSecret {
		t.Fatalf("SealAPIKey err = %v", err)
	}
	if _, err := s.OpenAPIKey(sealer.Prefix + "AAAA"); err != ErrEncryptionSecret {
		t.Fatalf("OpenAPIKey err = %v", err)
	}
	if got, err := s.OpenAPIKey("sk-plain"); err != nil || got != "sk-plain" {
		t.Fatalf("a plain key must pass through: %q %v", got, err)
	}
	if _, err := s.SealConnectionData("c", `{}`); err != ErrEncryptionSecret {
		t.Fatalf("SealConnectionData err = %v", err)
	}
	if _, err := s.SealSettings(`{}`); err != ErrEncryptionSecret {
		t.Fatalf("SealSettings err = %v", err)
	}
}

func TestOnlyAConnectionSecretStillPlainIsFoundAndSealed(t *testing.T) {
	s := newMigrated(t)
	s.exec(t, `INSERT INTO "providerConnections"("id","provider","authType","name","data","createdAt","updatedAt") VALUES ('c1','openai','apikey','main',?,'t','t')`, `{"apiKey":"sk-only-conn"}`)
	file := s.SQ.File
	s.SQ.Close()

	s2 := reopen(t, file, tests.DBSecret)
	if _, err := s2.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	names := backupNames(t, file)
	if len(names) != 1 || !strings.HasPrefix(names[0], preEncryptionLabel+"-") {
		t.Fatalf("a plain connection secret alone must trigger the pre-encryption backup: %v", names)
	}
	if data := s2.one(t, `SELECT "data" FROM "providerConnections"`); strings.Contains(data, "sk-only-conn") {
		t.Fatal("the connection secret is still plain")
	}
}

func TestOnlyASettingsSecretStillPlainIsFoundAndSealed(t *testing.T) {
	s := newMigrated(t)
	s.exec(t, `INSERT INTO "settings"("id","data") VALUES (1,?)`, `{"oidcClientSecret":"only-setting"}`)
	file := s.SQ.File
	s.SQ.Close()

	s2 := reopen(t, file, tests.DBSecret)
	if _, err := s2.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if names := backupNames(t, file); len(names) != 1 || !strings.HasPrefix(names[0], preEncryptionLabel+"-") {
		t.Fatalf("backups = %v", names)
	}
	if data := s2.one(t, `SELECT "data" FROM "settings"`); strings.Contains(data, "only-setting") {
		t.Fatal("the settings secret is still plain")
	}
}

func TestSealedSecretsOfADatabaseSealedByAnotherSecretAreCountedUnopenable(t *testing.T) {
	file := makeLegacy(t)
	s := reopen(t, file, tests.DBSecret)
	if _, err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.exec(t, `DELETE FROM "kv" WHERE "scope"='rllmCrypto'`)
	sp, err := reopen(t, file, tests.OtherDBSecret).scanSecrets(s.SQ.DB, false)
	if err != nil {
		t.Fatal(err)
	}
	if sp.unopenable != 5 {
		t.Fatalf("unopenable = %d", sp.unopenable)
	}
	if sp.values() != 0 {
		t.Fatalf("sealed values were counted as plain: %d", sp.values())
	}
}
