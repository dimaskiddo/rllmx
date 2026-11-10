package importer

import (
	"context"
	"errors"
	"rllmx/app/store/document"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"rllmx/tests"
)

func TestReadCountsEveryCopiedTable(t *testing.T) {
	c := read(t, Options{}).Counts()
	want := Counts{Connections: 2, APIKeys: 2, Nodes: 1, Combos: 1, ModelAliases: 1, CustomModels: 1, DisabledModels: 1, Pricing: 1, Settings: 6}
	if c != want {
		t.Fatalf("counts = %+v, want %+v", c, want)
	}
}

func TestConnectionsLoseLocksAndErrorStateButKeepEverythingElse(t *testing.T) {
	p := read(t, Options{})
	if len(p.connections) != 2 {
		t.Fatalf("%d connections", len(p.connections))
	}
	var got string
	for _, c := range p.connections {
		if c.id == "conn-1" {
			got = c.data
		}
	}
	if got == "" {
		t.Fatal("conn-1 is missing")
	}
	for _, gone := range []string{"testStatus", "lastError", "errorCode", "lastErrorAt", "backoffLevel", "rateLimitedUntil", "modelLock_gpt-5", "modelLock___all"} {
		if gjson.Get(got, gone).Exists() {
			t.Errorf("%s survived the clean: %s", gone, got)
		}
	}
	for _, kept := range []string{"apiKey", "stickyCount", "providerSpecificData"} {
		if !gjson.Get(got, kept).Exists() {
			t.Errorf("%s was dropped: %s", kept, got)
		}
	}
	if gjson.Get(got, "providerSpecificData.region").String() != "eu" {
		t.Errorf("a nested field changed: %s", got)
	}
	if strings.Index(got, `"apiKey"`) > strings.Index(got, `"stickyCount"`) {
		t.Errorf("the key order changed: %s", got)
	}
}

func TestKeyOrderAndUntouchedBytesSurviveCleaning(t *testing.T) {
	p := read(t, Options{})
	var got string
	for _, c := range p.connections {
		if c.id == "conn-1" {
			got = c.data
		}
	}
	if !strings.HasPrefix(got, `{"apiKey":"sk-upstream",`) {
		t.Fatalf("the leading bytes changed: %s", got)
	}
	// A number written as 2.0 must not come back as 2.
	src := `{"apiKey":"k","testStatus":"active","ratio":2.0}`
	cleaned, err := cleanConnection(src)
	if err != nil {
		t.Fatal(err)
	}
	if cleaned != `{"apiKey":"k","ratio":2.0}` {
		t.Fatalf("cleaned = %s", cleaned)
	}
}

func TestCorruptRowsAreSkippedAndCounted(t *testing.T) {
	path := source(t)
	sq := tests.OpenSQLite(t, path)
	defer sq.Close()
	for _, stmt := range []string{
		`INSERT INTO "providerConnections"("id","provider","authType","name","email","priority","isActive","data","createdAt","updatedAt") VALUES('bad','x','apikey',NULL,NULL,NULL,1,'{not json','t','t')`,
		`INSERT INTO "providerNodes"("id","type","name","data","createdAt","updatedAt") VALUES('bad','x',NULL,'{not json','t','t')`,
		`INSERT INTO "kv"("scope","key","value") VALUES('pricing','bad','not json')`,
	} {
		if err := sq.DB.Statement(stmt); err != nil {
			t.Fatal(err)
		}
	}
	p, err := Read(context.Background(), sq.DB, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Skipped.CorruptConnections != 1 || p.Skipped.CorruptNodes != 1 || p.Skipped.CorruptKV != 1 {
		t.Fatalf("skipped = %+v", p.Skipped)
	}
	if c := p.Counts(); c.Connections != 2 || c.Nodes != 1 || c.Pricing != 1 {
		t.Fatalf("counts = %+v", c)
	}
}

func TestSettingsAllowListKeepsSkillFlagsAndDropsSecurityAndInfra(t *testing.T) {
	p := read(t, Options{})
	var keys []string
	for _, s := range p.settings {
		keys = append(keys, s.key)
	}
	joined := strings.Join(keys, ",")
	for _, want := range []string{"stickyRoundRobinLimit", "providerStrategies", "cavemanEnabled", "cavemanRoutingMode", "ecc_auto_skill_routerEnabled", "taste-skillEnabled"} {
		if !strings.Contains(joined, want) {
			t.Errorf("%s was not copied: %v", want, keys)
		}
	}
	for _, skip := range []string{"authMode", "ssoType", "oidcClientSecret", "password", "requireLogin", "tunnelEnabled", "outboundProxyUrl", "enableObservability", "headroomAutoStart"} {
		if strings.Contains(joined, skip) {
			t.Errorf("%s was copied: %v", skip, keys)
		}
	}
	if strings.Join(p.Skipped.SecuritySettings, ",") != "authMode,oidcClientId,oidcClientSecret,oidcIssuerUrl,ssoType" {
		t.Errorf("security = %v", p.Skipped.SecuritySettings)
	}
	if strings.Join(p.Skipped.UnknownSettings, ",") != "someFutureKey" {
		t.Errorf("unknown = %v", p.Skipped.UnknownSettings)
	}
}

func TestWithSSOAddsOnlyTheSecurityPolicy(t *testing.T) {
	base := read(t, Options{})
	sso := read(t, Options{WithSSO: true})
	if sso.Counts().Settings != base.Counts().Settings+5 {
		t.Fatalf("%d settings with SSO, %d without", sso.Counts().Settings, base.Counts().Settings)
	}
	if len(sso.Skipped.SecuritySettings) != 0 {
		t.Fatalf("security still reported: %v", sso.Skipped.SecuritySettings)
	}
	var keys []string
	for _, s := range sso.settings {
		keys = append(keys, s.key)
	}
	joined := strings.Join(keys, ",")
	for _, want := range []string{"authMode", "ssoType", "oidcIssuerUrl", "oidcClientId", "oidcClientSecret"} {
		if !strings.Contains(joined, want) {
			t.Errorf("%s missing: %v", want, keys)
		}
	}
	if strings.Contains(joined, "password") || strings.Contains(joined, "tunnelEnabled") {
		t.Errorf("a skipped key came with --with-sso: %v", keys)
	}
}

// Every default this version knows must be classified, so a setting added later
// cannot slip through as "unknown" and silently stop being imported.
func TestEveryEffectiveSettingKeyIsClassified(t *testing.T) {
	d, err := emptyDest(t).Settings().Effective(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range d.Keys() {
		if classifySetting(k) == classUnknown {
			t.Errorf("setting %q is not classified: add it to copied, security or skipped", k)
		}
	}
}

func TestMissingAccessColumnsReadAsUnrestricted(t *testing.T) {
	sq := tests.NewSQLite(t)
	defer sq.Close()
	for _, stmt := range []string{
		`CREATE TABLE "apiKeys"("id" TEXT PRIMARY KEY, "key" TEXT NOT NULL UNIQUE, "name" TEXT, "machineId" TEXT, "isActive" INTEGER DEFAULT 1, "createdAt" TEXT NOT NULL)`,
		`INSERT INTO "apiKeys"("id","key","name","machineId","isActive","createdAt") VALUES('k','sk-old-333333-cccc',NULL,NULL,1,'2026-01-01T00:00:00.000Z')`,
	} {
		if err := sq.DB.Statement(stmt); err != nil {
			t.Fatal(err)
		}
	}
	p, err := Read(context.Background(), sq.DB, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.apiKeys) != 1 {
		t.Fatalf("%d keys", len(p.apiKeys))
	}
	k := p.apiKeys[0]
	if k.restricted != nil || k.allow != "" {
		t.Fatalf("key = %+v", k)
	}
}

func TestReadFailsOnADatabaseWithNoConfigurationTables(t *testing.T) {
	sq := tests.NewSQLite(t)
	defer sq.Close()
	if err := sq.DB.Statement(`CREATE TABLE "something"("x" TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(context.Background(), sq.DB, Options{}); !errors.Is(err, ErrNoConfiguration) {
		t.Fatalf("err = %v", err)
	}
}

func TestSealedSourceIsRefused(t *testing.T) {
	path := sealed(t, source(t))
	if _, err := Read(context.Background(), openSource(t, path), Options{}); !errors.Is(err, ErrSealedSource) {
		t.Fatalf("err = %v", err)
	}
}

// Each of the three places a secret lives is refused on its own, so one check
// cannot hide a missing other.
func TestASealedKeyAloneIsRefused(t *testing.T) {
	path := source(t)
	dest := emptyDest(t)
	sealedKey, err := dest.SealAPIKey("sk-alpha-111111-aaaa")
	if err != nil {
		t.Fatal(err)
	}
	sq := tests.OpenSQLite(t, path)
	defer sq.Close()
	if err := sq.DB.Statement(`UPDATE "apiKeys" SET "key" = ? WHERE "id" = 'key-1'`, sealedKey); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(context.Background(), sq.DB, Options{}); !errors.Is(err, ErrSealedSource) {
		t.Fatalf("err = %v", err)
	}
}

func TestSealedConnectionOrSettingsSourceIsRefused(t *testing.T) {
	for name, mutate := range map[string]string{
		"connection": `UPDATE "providerConnections" SET "data" = json_set("data", '$.apiKey', '` + sealedShaped + `') WHERE "id" = 'conn-1'`,
		"settings":   `UPDATE "settings" SET "data" = json_set("data", '$.oidcClientSecret', '` + sealedShaped + `') WHERE "id" = 1`,
	} {
		t.Run(name, func(t *testing.T) {
			path := source(t)
			sq := tests.OpenSQLite(t, path)
			defer sq.Close()
			if err := sq.DB.Statement(mutate); err != nil {
				t.Fatal(err)
			}
			if _, err := Read(context.Background(), sq.DB, Options{}); !errors.Is(err, ErrSealedSource) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

// A sealed-looking string that is not at a known secret path must still be
// caught: the whole document is scanned, not a list of fields.
func TestSealedValueAnywhereInADocumentIsRefused(t *testing.T) {
	path := source(t)
	sq := tests.OpenSQLite(t, path)
	defer sq.Close()
	if err := sq.DB.Statement(`UPDATE "providerConnections" SET "data" = json_set("data", '$.providerSpecificData.nested', ?) WHERE "id" = 'conn-1'`, sealedShaped); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(context.Background(), sq.DB, Options{}); !errors.Is(err, ErrSealedSource) {
		t.Fatalf("err = %v", err)
	}
}

// The report is pinned line for line: a count or a key name may appear, a
// value may not, and nothing else may be printed at all.
func TestTheReportNamesCountsAndSettingKeysButNeverValues(t *testing.T) {
	p := read(t, Options{})
	want := "2 provider connections, 2 API keys, 1 provider nodes, 1 combos\n" +
		"1 model aliases, 1 custom models, 1 disabled-model lists, 1 pricing overrides, 6 settings\n" +
		"login policy not copied (use --with-sso): authMode, oidcClientId, oidcClientSecret, oidcIssuerUrl, ssoType\n" +
		"settings this version does not know, not copied: someFutureKey\n"
	if got := p.Report(); got != want {
		t.Fatalf("report:\n%s\nwant:\n%s", got, want)
	}
}

func TestReadReportsAQueryFailureInsteadOfSkippingTheTable(t *testing.T) {
	sq := tests.NewSQLite(t)
	defer sq.Close()
	if err := sq.DB.Statement(`CREATE TABLE "settings"("id" INTEGER, "wrong" TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err := sq.DB.Statement(`CREATE TABLE "apiKeys"("id" TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(context.Background(), sq.DB, Options{}); err == nil {
		t.Fatal("a broken table read as empty")
	}
}

func TestCleanConnectionKeepsAnEmptyObjectAndRefusesBrokenText(t *testing.T) {
	got, err := cleanConnection(`{}`)
	if err != nil || got != `{}` {
		t.Fatalf("got %q %v", got, err)
	}
	if _, err := cleanConnection(`[]`); !errors.Is(err, document.ErrCorruptRow) {
		t.Fatalf("err = %v", err)
	}
}

const sealedShaped = "enc:v1:AAAA"
