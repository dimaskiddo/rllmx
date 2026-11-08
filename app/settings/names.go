// Package settings validates the configuration names and values that the
// framework's loader accepts without question. The loader is viper's, so a
// misspelled variable in .env is silently ignored; this package refuses it.
package settings

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

var namePattern = regexp.MustCompile(`^[A-Z]+(_[A-Z0-9]+)+$`)

// ownedGroups are the prefixes whose variables the product owns. An entry in
// one of them that the code does not read is a typo, not another program's name.
var ownedGroups = []string{
	"SERVER_", "HTTP_", "AUTH_", "DB_", "ANALYTICS_", "CACHE_", "LOG_", "STREAM_",
	"UPSTREAM_", "OAUTH_", "MEDIA_", "SAVER_", "CATALOG_", "TRANSLATOR_", "PROVIDER_",
}

// known lists every variable a config file reads. A variable joins the list in
// the same change that starts reading it; a test compares the two directions.
var known = []string{
	"CACHE_RESPONSE_MAX_ENTRIES", "CACHE_RESPONSE_MAX_MB",
	"APP_DEBUG", "APP_ENV", "APP_HOST", "APP_KEY", "APP_NAME", "APP_PORT", "APP_URL",
	"ANALYTICS_RETENTION_DAYS",
	"AUTH_API_KEY_SECRET", "AUTH_COOKIE_SECURE", "AUTH_INITIAL_PASSWORD",
	"AUTH_MACHINE_ID_SALT", "AUTH_REFUSE_DEFAULT_PASSWORD", "AUTH_SSO_ALLOW",
	"DATA_DIR", "JWT_SECRET",
	"DB_CONNECTION", "DB_DATABASE", "DB_DATABASE_ANALYTICS", "DB_ENCRYPTION_SECRET", "DB_HOST",
	"DB_PASSWORD", "DB_PORT", "DB_SCHEMA", "DB_SSLMODE", "DB_USERNAME",
	"HTTP_SERVER_IDLE_TIMEOUT", "HTTP_SERVER_READ_HEADER_TIMEOUT", "HTTP_SERVER_SHUTDOWN_GRACE",
	"LOG_CHANNEL", "LOG_LEVEL",
	"REDIS_DB", "REDIS_HOST", "REDIS_PASSWORD", "REDIS_PORT",
	"SERVER_MAX_BODY_MB", "SERVER_TRUSTED_PROXIES", "SERVER_TRUST_LOOPBACK",
	"STREAM_FIRST_CHUNK_TIMEOUT", "STREAM_STALL_TIMEOUT", "STREAM_USAGE_WAIT",
	"UPSTREAM_ALLOW_PRIVATE", "UPSTREAM_CONNECT_TIMEOUT",
}

// Known returns the variable names the code reads, sorted.
func Known() []string { return slices.Sorted(slices.Values(known)) }

// CheckNames refuses a name in an owned group that the code does not read, and
// a lower-case or mixed-case spelling of a name it does. Other names pass: the
// file may be shared with other programs. The error names the variable only.
func CheckNames(names []string) error {
	for _, name := range names {
		if strings.HasPrefix(name, "=") {
			continue
		}
		upper := strings.ToUpper(name)
		if slices.Contains(known, name) {
			continue
		}
		if slices.Contains(known, upper) || hasOwnedPrefix(upper) {
			return fmt.Errorf("settings: unknown variable %s", name)
		}
	}
	return nil
}

func hasOwnedPrefix(upper string) bool {
	return slices.ContainsFunc(ownedGroups, func(g string) bool { return strings.HasPrefix(upper, g) })
}
