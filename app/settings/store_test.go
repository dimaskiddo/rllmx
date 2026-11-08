package settings

import (
	"maps"
	"strings"
	"testing"

	"github.com/goravel/framework/contracts/config"

	"rllmx/tests"
)

const goodSecret = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"

func storeValues(over map[string]any) tests.Config {
	m := map[string]any{
		"database.encryption_secret":           goodSecret,
		"database.analytics.retention_days":    90,
		"database.connections.sqlite.database": "/var/lib/rllmx/db/data.sqlite",
		"database.default":                     "sqlite",
		"app.data_dir":                         "/var/lib/rllmx",
	}
	maps.Copy(m, over)
	return tests.Config{Values: m}
}

func TestLoadStoreReadsTheSecretAndTheRetention(t *testing.T) {
	s, err := loadServer(storeValues(nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Secret) != 32 || s.Secret[0] != 0 || s.Secret[31] != 0x1f {
		t.Fatalf("secret bytes = %x", s.Secret)
	}
	if s.RetentionDays != 90 {
		t.Fatalf("retention = %d", s.RetentionDays)
	}
}

func TestTheSecretIsRequiredToOpenAStore(t *testing.T) {
	_, err := loadServer(storeValues(map[string]any{"database.encryption_secret": ""}))
	if err == nil || !strings.Contains(err.Error(), "DB_ENCRYPTION_SECRET") {
		t.Fatalf("error = %v", err)
	}
}

func TestACheckWithoutTheSecretStillWorks(t *testing.T) {
	if err := CheckSecretFormat(storeValues(map[string]any{"database.encryption_secret": ""})); err != nil {
		t.Fatalf("an empty secret is allowed at boot (version and health need none): %v", err)
	}
	if err := CheckSecretFormat(storeValues(nil)); err != nil {
		t.Fatal(err)
	}
}

func TestABadSecretIsRefusedAtBootAndNeverQuoted(t *testing.T) {
	const marker = "zz-secret-marker-value"
	for _, bad := range []string{marker, "00", goodSecret + "00", strings.ToUpper(goodSecret)[:63] + "G", "0x" + goodSecret[2:]} {
		for name, fn := range map[string]func(config.Config) error{
			"boot":  CheckSecretFormat,
			"store": func(c config.Config) error { _, err := loadServer(c); return err },
		} {
			err := fn(storeValues(map[string]any{"database.encryption_secret": bad}))
			if err == nil || !strings.Contains(err.Error(), "DB_ENCRYPTION_SECRET") {
				t.Errorf("%s %q: error = %v", name, bad, err)
				continue
			}
			if strings.Contains(err.Error(), bad) && len(bad) > 4 || strings.Contains(err.Error(), marker) {
				t.Errorf("%s: the error repeats the secret: %v", name, err)
			}
		}
	}
}

func TestUpperCaseHexIsAccepted(t *testing.T) {
	if _, err := loadServer(storeValues(map[string]any{"database.encryption_secret": strings.ToUpper(goodSecret)})); err != nil {
		t.Fatal(err)
	}
}

func TestRetentionBounds(t *testing.T) {
	for _, ok := range []int{1, 90, 36500} {
		if _, err := loadServer(storeValues(map[string]any{"database.analytics.retention_days": ok})); err != nil {
			t.Errorf("%d rejected: %v", ok, err)
		}
	}
	for _, bad := range []int{0, -1, 36501} {
		_, err := loadServer(storeValues(map[string]any{"database.analytics.retention_days": bad}))
		if err == nil || !strings.Contains(err.Error(), "ANALYTICS_RETENTION_DAYS") {
			t.Errorf("%d: error = %v", bad, err)
		}
	}
}

func TestTheDatabaseSettingsNameTheEngineTheFolderAndTheSchema(t *testing.T) {
	s, err := loadServer(storeValues(nil))
	if err != nil {
		t.Fatal(err)
	}
	if s.Engine != "sqlite" || s.DataDir != "/var/lib/rllmx" || s.PGSchema != "" || s.File != "/var/lib/rllmx/db/data.sqlite" {
		t.Fatalf("store = %+v", s)
	}
	pg, err := loadServer(storeValues(map[string]any{"database.default": "postgres", "database.connections.postgres.schema": "rllmx"}))
	if err != nil || pg.Engine != "postgres" || pg.PGSchema != "rllmx" || pg.File != "" {
		t.Fatalf("postgres store = %+v %v", pg, err)
	}
}

func TestOnlySQLiteAndPostgresAreEngines(t *testing.T) {
	for _, bad := range []string{"mysql", "sqlite_ro", "sqlite_analytics", "SQLITE", " sqlite"} {
		values := storeValues(map[string]any{"database.default": bad})
		if err := CheckConnection(values); err == nil || !strings.Contains(err.Error(), "DB_CONNECTION") {
			t.Errorf("%q: CheckConnection err = %v", bad, err)
		}
		if _, err := loadServer(values); err == nil || !strings.Contains(err.Error(), "DB_CONNECTION") {
			t.Errorf("%q: LoadStore err = %v", bad, err)
		}
	}
	for _, ok := range []string{"sqlite", "postgres"} {
		if err := CheckConnection(storeValues(map[string]any{"database.default": ok})); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
}

func TestADatabaseCommandNeedsNoSecret(t *testing.T) {
	s, err := LoadDatabase(storeValues(map[string]any{"database.encryption_secret": ""}))
	if err != nil {
		t.Fatalf("backup, check and vacuum never touch a secret: %v", err)
	}
	if len(s.Secret) != 0 {
		t.Fatalf("secret = %x", s.Secret)
	}
	if _, err := LoadDatabase(storeValues(map[string]any{"database.encryption_secret": "nothex"})); err == nil {
		t.Fatal("a secret that is set but malformed was accepted")
	}
	if _, err := LoadDatabase(storeValues(map[string]any{"app.data_dir": ""})); err == nil || !strings.Contains(err.Error(), "DATA_DIR") {
		t.Fatalf("err = %v", err)
	}
}

func TestRequireSecretNamesTheVariableAndNeverTheValue(t *testing.T) {
	none, err := LoadDatabase(storeValues(map[string]any{"database.encryption_secret": ""}))
	if err != nil {
		t.Fatal(err)
	}
	if err := none.RequireSecret(); err == nil || !strings.Contains(err.Error(), "DB_ENCRYPTION_SECRET") {
		t.Fatalf("err = %v", err)
	}
	have, err := LoadDatabase(storeValues(nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := have.RequireSecret(); err != nil {
		t.Fatalf("a store with a secret was refused: %v", err)
	}
}

// loadServer is what the server does: read the database settings, then require
// the secret before anything seals or opens a value.
func loadServer(cfg config.Config) (*Store, error) {
	s, err := LoadDatabase(cfg)
	if err != nil {
		return nil, err
	}
	if err := s.RequireSecret(); err != nil {
		return nil, err
	}
	return s, nil
}
