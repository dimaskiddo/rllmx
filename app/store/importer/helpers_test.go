package importer

import (
	"context"
	"testing"

	contractsdb "github.com/goravel/framework/contracts/database/db"

	"rllmx/tests"
	"rllmx/tests/store"
)

// legacyDDL is the configuration half of a legacy database: the tables the
// import reads, with their camelCase columns and the two columns an older
// source may lack.
const legacyDDL = `CREATE TABLE "_meta" ("key" TEXT PRIMARY KEY, "value" TEXT NOT NULL);
CREATE TABLE "settings" ("id" INTEGER PRIMARY KEY CHECK (id = 1), "data" TEXT NOT NULL);
CREATE TABLE "providerConnections" ("id" TEXT PRIMARY KEY, "provider" TEXT NOT NULL, "authType" TEXT NOT NULL, "name" TEXT, "email" TEXT, "priority" INTEGER, "isActive" INTEGER DEFAULT 1, "data" TEXT NOT NULL, "createdAt" TEXT NOT NULL, "updatedAt" TEXT NOT NULL);
CREATE TABLE "providerNodes" ("id" TEXT PRIMARY KEY, "type" TEXT, "name" TEXT, "data" TEXT NOT NULL, "createdAt" TEXT NOT NULL, "updatedAt" TEXT NOT NULL);
CREATE TABLE "apiKeys" ("id" TEXT PRIMARY KEY, "key" TEXT NOT NULL UNIQUE, "name" TEXT, "machineId" TEXT, "isActive" INTEGER DEFAULT 1, "createdAt" TEXT NOT NULL, "accessRestricted" INTEGER DEFAULT 0, "accessAllow" TEXT);
CREATE TABLE "combos" ("id" TEXT PRIMARY KEY, "name" TEXT NOT NULL UNIQUE, "kind" TEXT, "models" TEXT NOT NULL, "createdAt" TEXT NOT NULL, "updatedAt" TEXT NOT NULL);
CREATE TABLE "kv" ("scope" TEXT NOT NULL, "key" TEXT NOT NULL, "value" TEXT NOT NULL, PRIMARY KEY ("scope","key"));
CREATE TABLE "usageHistory" ("id" INTEGER PRIMARY KEY AUTOINCREMENT, "timestamp" TEXT NOT NULL, "provider" TEXT, "apiKey" TEXT);
CREATE TABLE "requestDetails" ("id" INTEGER PRIMARY KEY AUTOINCREMENT, "timestamp" TEXT, "provider" TEXT);
CREATE TABLE "proxyPoolFitness" ("poolId" TEXT NOT NULL, "scope" TEXT NOT NULL, "until" TEXT, PRIMARY KEY ("poolId","scope"));`

// source builds a legacy database with one row of every kind the import copies
// and a few it must leave behind. It returns the file path.
func source(t testing.TB) string {
	t.Helper()
	sq := tests.NewSQLite(t)
	exec := func(q string, args ...any) {
		t.Helper()
		if err := sq.DB.Statement(q, args...); err != nil {
			t.Fatalf("source: %s: %v", q, err)
		}
	}
	for _, stmt := range tests.SplitStatements(legacyDDL) {
		exec(stmt)
	}
	exec(`INSERT INTO "_meta"("key","value") VALUES('totalRequestsLifetime','777')`)
	exec(`INSERT INTO "settings"("id","data") VALUES(1,?)`, settingsDoc)
	exec(`INSERT INTO "providerConnections"("id","provider","authType","name","email","priority","isActive","data","createdAt","updatedAt") VALUES(?,?,?,?,?,?,?,?,?,?)`,
		"conn-1", "openai", "apikey", "work", "w@example.test", 1, 1, connData, "2026-01-01T00:00:00.000Z", "2026-01-02T00:00:00.000Z")
	exec(`INSERT INTO "providerConnections"("id","provider","authType","name","email","priority","isActive","data","createdAt","updatedAt") VALUES(?,?,?,?,?,?,?,?,?,?)`,
		"conn-2", "anthropic", "oauth", nil, nil, nil, nil, `{"accessToken":"tok","expiresAt":1}`, "2026-01-03T00:00:00.000Z", "2026-01-03T00:00:00.000Z")
	exec(`INSERT INTO "apiKeys"("id","key","name","machineId","isActive","createdAt","accessRestricted","accessAllow") VALUES(?,?,?,?,?,?,?,?)`,
		"key-1", "sk-alpha-111111-aaaa", "ci", "m1", 1, "2026-01-01T00:00:00.000Z", 1, `["openai/*"]`)
	exec(`INSERT INTO "apiKeys"("id","key","name","machineId","isActive","createdAt","accessRestricted","accessAllow") VALUES(?,?,?,?,?,?,?,?)`,
		"key-2", "sk-beta-222222-bbbb", nil, nil, 0, "2026-01-02T00:00:00.000Z", 0, nil)
	exec(`INSERT INTO "providerNodes"("id","type","name","data","createdAt","updatedAt") VALUES(?,?,?,?,?,?)`,
		"node-1", "openai-compatible", "local", `{"prefix":"dke","apiType":"chat","baseUrl":"https://x.test/v1"}`, "2026-01-01T00:00:00.000Z", "2026-01-01T00:00:00.000Z")
	exec(`INSERT INTO "combos"("id","name","kind","models","createdAt","updatedAt") VALUES(?,?,?,?,?,?)`,
		"combo-1", "fast", nil, `["openai/gpt-5"]`, "2026-01-01T00:00:00.000Z", "2026-01-01T00:00:00.000Z")
	exec(`INSERT INTO "kv"("scope","key","value") VALUES('modelAliases','opus','"openai/gpt-5"')`)
	exec(`INSERT INTO "kv"("scope","key","value") VALUES('customModels','oa|x|llm','{"providerAlias":"oa","id":"x","type":"llm"}')`)
	exec(`INSERT INTO "kv"("scope","key","value") VALUES('disabledModels','oa','["gpt-4"]')`)
	exec(`INSERT INTO "kv"("scope","key","value") VALUES('pricing','openai','{"gpt-5":{"input":1}}')`)
	exec(`INSERT INTO "kv"("scope","key","value") VALUES('mitmAlias','claude','{"a":"b"}')`)
	exec(`INSERT INTO "usageHistory"("timestamp","provider","apiKey") VALUES('2026-01-01T00:00:00.000Z','openai','sk-alpha-111111-aaaa')`)
	sq.Close()
	return sq.File
}

const settingsDoc = `{"stickyRoundRobinLimit":5,"providerStrategies":{"openai":"sticky"},"cavemanEnabled":true,` +
	`"cavemanRoutingMode":"auto","ecc_auto_skill_routerEnabled":true,"taste-skillEnabled":true,` +
	`"authMode":"oidc","ssoType":"oidc","oidcIssuerUrl":"https://idp.test","oidcClientId":"cid","oidcClientSecret":"sec",` +
	`"password":"$2a$10$hash","requireLogin":true,"tunnelEnabled":true,"outboundProxyUrl":"http://p.test",` +
	`"enableObservability":false,"headroomAutoStart":true,"someFutureKey":1}`

// connData keeps a lock, the whole error state and fields the import must not
// touch, in a fixed key order, so a byte comparison means something.
const connData = `{"apiKey":"sk-upstream","testStatus":"active","lastError":"boom","errorCode":"E1",` +
	`"lastErrorAt":"2026-01-02T00:00:00.000Z","backoffLevel":3,"rateLimitedUntil":"2026-01-03T00:00:00.000Z",` +
	`"modelLock_gpt-5":"2026-01-02T00:00:00.000Z","modelLock___all":"2026-01-02T00:00:00.000Z",` +
	`"stickyCount":2,"providerSpecificData":{"connectionProxyEnabled":false,"region":"eu"}}`

func openSource(t testing.TB, path string) contractsdb.DB {
	t.Helper()
	sq := tests.OpenSQLite(t, path)
	t.Cleanup(func() { _ = sq.Close() })
	return sq.DB
}

// emptyDest is a migrated destination database with no configuration rows.
func emptyDest(t testing.TB) *store.Store { return store.Migrated(t) }

func read(t testing.TB, opt Options) *Plan {
	t.Helper()
	path := source(t)
	p, err := Read(context.Background(), openSource(t, path), opt)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return p
}

// sealed rewrites every plain secret of a source file in place, the way the
// server would have. It is the state a source of a second import is in.
func sealed(t testing.TB, path string) string {
	t.Helper()
	st := store.Over(t, tests.OpenSQLite(t, path), tests.DBSecret)
	if _, err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("seal: %v", err)
	}
	return path
}
