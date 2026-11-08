package repository

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/sealer"
	"rllmx/tests"
)

func TestAPIKeyIsStoredSealedAndStillFound(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	const plain = "sk-m1-aaaaaa-11111111"
	k, err := s.APIKeys().Create(ctx, "ci", "m1", plain)
	if err != nil {
		t.Fatal(err)
	}
	raw := s.one(t, `SELECT "key" FROM "apiKeys" WHERE "id" = ?`, k.ID)
	if !sealer.IsSealed(raw) || strings.Contains(raw, plain) {
		t.Fatalf("stored key = %q", raw)
	}
	got, found, err := s.APIKeys().GetByKey(ctx, plain)
	if err != nil || !found || got.ID != k.ID || got.Key != plain {
		t.Fatalf("GetByKey = %+v %v %v", got, found, err)
	}
	if _, found, _ := s.APIKeys().GetByKey(ctx, plain+"x"); found {
		t.Fatal("a different key was found")
	}
	list, _ := s.APIKeys().List(ctx)
	if len(list) != 1 || list[0].Key != plain || list[0].Unreadable {
		t.Fatalf("List = %+v", list)
	}
	if _, err := s.APIKeys().Create(ctx, "dup", "m1", plain); err == nil {
		t.Fatal("the unique index no longer rejects a duplicate key")
	}
}

func TestLookingUpAnUnknownKeyLeavesNoKeyInTheLog(t *testing.T) {
	s := newMigrated(t)
	const presented = "sk-presented-by-a-stranger-1234"
	if _, found, err := s.APIKeys().GetByKey(context.Background(), presented); err != nil || found {
		t.Fatalf("found %v err %v", found, err)
	}
	sealed, _ := s.SealAPIKey(presented)
	for _, line := range s.SQ.Log.Lines() {
		if strings.Contains(line, presented) || strings.Contains(line, sealed) {
			t.Fatalf("a presented key reached the log: %q", line)
		}
	}
}

func TestAPIKeyUpdateKeepsTheStoredCiphertext(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	k, _ := s.APIKeys().Create(ctx, "ci", "m1", "sk-m1-aaaaaa-11111111")
	before := s.one(t, `SELECT "key" FROM "apiKeys" WHERE "id" = ?`, k.ID)
	name := "renamed"
	if _, err := s.APIKeys().Update(ctx, k.ID, APIKeyPatch{Name: &name}); err != nil {
		t.Fatal(err)
	}
	if s.one(t, `SELECT "key" FROM "apiKeys" WHERE "id" = ?`, k.ID) != before {
		t.Fatal("an update changed the key column")
	}
}

func TestAPIKeyMovedToAnotherPlaceDoesNotOpen(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	k, _ := s.APIKeys().Create(ctx, "ci", "m1", "sk-m1-aaaaaa-11111111")
	moved := tests.Box(t, tests.DBSecret).Seal("rllm/v1/providerConnections/x/apiKey", "sk-m1-aaaaaa-11111111")
	s.exec(t, `UPDATE "apiKeys" SET "key"=? WHERE "id"=?`, moved, k.ID)
	list, err := s.APIKeys().List(ctx)
	if err != nil || len(list) != 1 || !list[0].Unreadable || list[0].Key != "" {
		t.Fatalf("List = %+v %v", list, err)
	}
}

func TestConnectionSecretsAreSealedInTheDataColumn(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	c, err := s.Connections().Create(ctx, ConnInput{Provider: "openai", AuthType: "apikey", Name: "main",
		Fields: map[string]string{"apiKey": `"sk-provider"`, "accessToken": `"at"`, "refreshToken": `"rt"`, "idToken": `"it"`, "displayName": `"Main"`},
		PSD:    `{"secretAccessKey":"aws-secret","accessKeyId":"aws-id","region":"eu"}`}, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw := s.one(t, `SELECT "data" FROM "providerConnections" WHERE "id" = ?`, c.ID)
	for _, plain := range []string{"sk-provider", `"at"`, `"rt"`, `"it"`, "aws-secret", "aws-id"} {
		if strings.Contains(raw, plain) {
			t.Fatalf("stored data holds %s", plain)
		}
	}
	for _, kept := range []string{`"displayName":"Main"`, `"region":"eu"`} {
		if !strings.Contains(raw, kept) {
			t.Fatalf("stored data lost %s: %s", kept, raw)
		}
	}
	got, ok, err := s.Connections().Get(ctx, c.ID)
	if err != nil || !ok || got.Corrupt {
		t.Fatalf("Get = %+v %v %v", got, ok, err)
	}
	if v, _ := got.Doc().GetString("apiKey"); v != "sk-provider" {
		t.Fatalf("apiKey = %q", v)
	}
	if v := gjson.Get(got.Data, "providerSpecificData.secretAccessKey").String(); v != "aws-secret" {
		t.Fatalf("secretAccessKey = %q", v)
	}
}

func TestUnrelatedUpdateKeepsSecretCiphertextBytes(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	c := mkConn(t, s, ConnInput{Provider: "openai", AuthType: "apikey", Name: "main",
		Fields: map[string]string{"apiKey": `"sk-provider"`}, PSD: `{"cookie":"c-1"}`})
	before := s.one(t, `SELECT "data" FROM "providerConnections" WHERE "id" = ?`, c.ID)
	if err := s.Connections().TouchUsageAt(ctx, c.ID, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	after := s.one(t, `SELECT "data" FROM "providerConnections" WHERE "id" = ?`, c.ID)
	for _, p := range []string{"apiKey", "providerSpecificData.cookie"} {
		if gjson.Get(before, p).String() != gjson.Get(after, p).String() {
			t.Fatalf("%s ciphertext changed on an unrelated write", p)
		}
	}
	if before == after {
		t.Fatal("the touch wrote nothing")
	}
}

func TestChangedSecretIsResealed(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	c := mkConn(t, s, ConnInput{Provider: "openai", AuthType: "apikey", Name: "main", Fields: map[string]string{"apiKey": `"sk-old"`}})
	before := gjson.Get(s.one(t, `SELECT "data" FROM "providerConnections" WHERE "id" = ?`, c.ID), "apiKey").String()
	if _, err := s.Connections().Update(ctx, c.ID, ConnPatch{Fields: map[string]string{"apiKey": `"sk-new"`}}); err != nil {
		t.Fatal(err)
	}
	raw := s.one(t, `SELECT "data" FROM "providerConnections" WHERE "id" = ?`, c.ID)
	after := gjson.Get(raw, "apiKey").String()
	if after == before || !sealer.IsSealed(after) || strings.Contains(raw, "sk-new") {
		t.Fatalf("after = %q", after)
	}
	got, _, _ := s.Connections().Get(ctx, c.ID)
	if v, _ := got.Doc().GetString("apiKey"); v != "sk-new" {
		t.Fatalf("apiKey = %q", v)
	}
}

func TestSecretCopiedToAnotherRowMakesItCorrupt(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	a := mkConn(t, s, ConnInput{Provider: "openai", AuthType: "apikey", Name: "a", Fields: map[string]string{"apiKey": `"sk-a"`}})
	b := mkConn(t, s, ConnInput{Provider: "openai", AuthType: "apikey", Name: "b", Fields: map[string]string{"apiKey": `"sk-b"`}})
	s.exec(t, `UPDATE "providerConnections" SET "data"=? WHERE "id"=?`, s.one(t, `SELECT "data" FROM "providerConnections" WHERE "id" = ?`, a.ID), b.ID)
	got, _, err := s.Connections().Get(ctx, b.ID)
	if err != nil || !got.Corrupt {
		t.Fatalf("a copied secret opened: corrupt=%v err=%v", got.Corrupt, err)
	}
	if _, err := s.Connections().Update(ctx, b.ID, ConnPatch{Fields: map[string]string{"apiKey": `"x"`}}); err == nil {
		t.Fatal("a corrupt row accepted a write")
	}
	if s.one(t, `SELECT "data" FROM "providerConnections" WHERE "id" = ?`, b.ID) != s.one(t, `SELECT "data" FROM "providerConnections" WHERE "id" = ?`, a.ID) {
		t.Fatal("a corrupt row was rewritten")
	}
}

func TestSettingsSecretIsSealed(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	if err := s.Settings().Patch(ctx, map[string]string{"oidcClientSecret": `"idp-secret"`, "oidcClientId": `"cid"`}); err != nil {
		t.Fatal(err)
	}
	raw := s.one(t, `SELECT "data" FROM "settings" WHERE "id" = 1`)
	if strings.Contains(raw, "idp-secret") || !sealer.IsSealed(gjson.Get(raw, "oidcClientSecret").String()) || !strings.Contains(raw, `"oidcClientId":"cid"`) {
		t.Fatalf("stored settings = %s", raw)
	}
	eff, err := s.Settings().Effective(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := eff.GetString("oidcClientSecret"); v != "idp-secret" {
		t.Fatalf("effective secret = %q", v)
	}
	before := gjson.Get(raw, "oidcClientSecret").String()
	if err := s.Settings().Patch(ctx, map[string]string{"cavemanEnabled": `true`}); err != nil {
		t.Fatal(err)
	}
	if gjson.Get(s.one(t, `SELECT "data" FROM "settings" WHERE "id" = 1`), "oidcClientSecret").String() != before {
		t.Fatal("an unrelated setting change re-sealed the secret")
	}
}

func TestReposNeedASecret(t *testing.T) {
	file := makeLegacy(t)
	s := reopen(t, file, "")
	ctx := context.Background()
	if _, err := s.APIKeys().Create(ctx, "n", "m", "sk-x"); err != ErrEncryptionSecret {
		t.Fatalf("Create key err = %v", err)
	}
	if _, _, err := s.APIKeys().GetByKey(ctx, "sk-x"); err != ErrEncryptionSecret {
		t.Fatalf("GetByKey err = %v", err)
	}
	if _, err := s.Connections().Create(ctx, ConnInput{Provider: "p", AuthType: "apikey", Name: "n"}, nil); err != ErrEncryptionSecret {
		t.Fatalf("Create connection err = %v", err)
	}
	if err := s.Settings().Patch(ctx, map[string]string{"a": `1`}); !errors.Is(err, ErrEncryptionSecret) {
		t.Fatalf("Patch err = %v", err)
	}
}
