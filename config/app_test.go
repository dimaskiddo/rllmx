package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"rllmx/app/facades"
	"rllmx/tests"
)

func envFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCheckAcceptsAKnownFileAndValidSettings(t *testing.T) {
	if err := check(envFile(t, "APP_PORT=20128\nHTTP_SERVER_IDLE_TIMEOUT=30s\n"), tests.Config{Values: tests.ServerValues()}); err != nil {
		t.Fatal(err)
	}
	if err := check(filepath.Join(t.TempDir(), "absent.env"), tests.Config{Values: tests.ServerValues()}); err != nil {
		t.Fatalf("no .env file is fine: %v", err)
	}
}

func TestCheckRefusesAnUnknownNameAndNamesIt(t *testing.T) {
	err := check(envFile(t, "SERVER_MAX_BODY=1\n"), tests.Config{Values: tests.ServerValues()})
	if err == nil || !strings.Contains(err.Error(), "SERVER_MAX_BODY") {
		t.Fatalf("error = %v", err)
	}
}

func TestCheckRefusesASettingOutOfRange(t *testing.T) {
	values := tests.ServerValues()
	values["http.server.idle_timeout"] = "0s"
	err := check(envFile(t, ""), tests.Config{Values: values})
	if err == nil || !strings.Contains(err.Error(), "HTTP_SERVER_IDLE_TIMEOUT") {
		t.Fatalf("error = %v", err)
	}
}

func TestCheckRefusesABadRedisPortOnlyWhenRedisIsOn(t *testing.T) {
	values := tests.ServerValues()
	values["database.redis.default.port"] = "70000"
	if err := check(envFile(t, ""), tests.Config{Values: values}); err != nil {
		t.Fatalf("a port nobody uses must not stop the start: %v", err)
	}
	values["database.redis.default.host"] = "127.0.0.1"
	err := check(envFile(t, ""), tests.Config{Values: values})
	if err == nil || !strings.Contains(err.Error(), "REDIS_PORT") {
		t.Fatalf("error = %v", err)
	}
}

func TestCheckRefusesAnUnresolvableDataDir(t *testing.T) {
	values := tests.ServerValues()
	values["app.data_dir"] = "~someone/x"
	err := check(envFile(t, ""), tests.Config{Values: values})
	if err == nil || !strings.Contains(err.Error(), "DATA_DIR") {
		t.Fatalf("error = %v", err)
	}
}

func TestTheDataDirectoryIsAbsoluteInTheRealConfig(t *testing.T) {
	got := facades.Config().GetString("app.data_dir")
	if !filepath.IsAbs(got) {
		t.Fatalf("app.data_dir = %q", got)
	}
}

// The real config is loaded by the framework when this package is imported, so
// these read what the application would see.
func TestTheFrameworksHTTPRunnerIsDisabled(t *testing.T) {
	if !slices.Contains(facades.Config().GetStringSlice("app.disabled_runners"), "goravel:http") {
		t.Fatal("goravel:http would start next to rllmx:http and fight it for the port")
	}
}

func TestTheServerDefaultsLoadFromTheRealConfig(t *testing.T) {
	cfg := facades.Config()
	for key, want := range map[string]string{
		"http.server.read_header_timeout": "10s",
		"http.server.idle_timeout":        "120s",
		"http.server.shutdown_grace":      "30s",
	} {
		if got := cfg.GetString(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if cfg.GetInt("http.request_timeout", -1) != 0 {
		t.Error("the framework's timeout middleware would buffer and cut a stream")
	}
	if cfg.GetInt("http.drivers.gin.header_limit") != 64 {
		t.Error("header limit is not 64 KiB")
	}
	if cfg.GetInt("hashing.bcrypt.rounds") != 10 {
		t.Error("bcrypt cost is part of the stored password format")
	}
}

func TestCheckRefusesAMalformedSecretButNotAMissingOne(t *testing.T) {
	values := tests.ServerValues()
	values["database.encryption_secret"] = "not-hex"
	err := check(envFile(t, ""), tests.Config{Values: values})
	if err == nil || !strings.Contains(err.Error(), "DB_ENCRYPTION_SECRET") || strings.Contains(err.Error(), "not-hex") {
		t.Fatalf("error = %v", err)
	}
	values["database.encryption_secret"] = ""
	if err := check(envFile(t, ""), tests.Config{Values: values}); err != nil {
		t.Fatalf("version and health must work without the secret: %v", err)
	}
}

func TestCheckRefusesAConnectionThatIsNotAnEngine(t *testing.T) {
	values := tests.ServerValues()
	values["database.default"] = "sqlite_analytics"
	err := check(envFile(t, ""), tests.Config{Values: values})
	if err == nil || !strings.Contains(err.Error(), "DB_CONNECTION") {
		t.Fatalf("error = %v", err)
	}
	values["database.default"] = "sqlite"
	if err := check(envFile(t, ""), tests.Config{Values: values}); err != nil {
		t.Fatalf("the default engine was refused: %v", err)
	}
}
