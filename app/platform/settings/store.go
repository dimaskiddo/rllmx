package settings

import (
	"encoding/hex"
	"errors"

	"github.com/goravel/framework/contracts/config"
)

const (
	secretKey     = "database.encryption_secret"
	retentionKey  = "database.analytics.retention_days"
	defaultKey    = "database.default"
	pgSchemaKey   = "database.connections.postgres.schema"
	sqliteFileKey = "database.connections.sqlite.database"
	dataDirKey    = "app.data_dir"
	minRetention  = 1
	maxRetention  = 36500
	secretHexLen  = 64
	secretByteLen = secretHexLen / 2

	// EngineSQLite and EnginePostgres are the two values of DB_CONNECTION.
	EngineSQLite   = "sqlite"
	EnginePostgres = "postgres"
)

var (
	errSecretRequired = errors.New("settings: DB_ENCRYPTION_SECRET: required to open the database; make one with: openssl rand -hex 32")
	errSecretFormat   = errors.New("settings: DB_ENCRYPTION_SECRET: must be exactly 64 hex characters")
	errEngine         = errors.New("settings: DB_CONNECTION: must be sqlite or postgres")
)

// Store is the validated view of the settings the data stores need.
type Store struct {
	Notices

	// Secret is the 32-byte key behind DB_ENCRYPTION_SECRET, empty for a
	// command that never touches a secret. Treat it as a secret: it is never
	// printed or logged.
	Secret []byte
	// RetentionDays is how long per-request usage rows are kept.
	RetentionDays int
	// Engine is EngineSQLite or EnginePostgres.
	Engine string
	// PGSchema is the PostgreSQL schema the tables live in; empty on SQLite.
	PGSchema string
	// DataDir is DATA_DIR as an absolute path.
	DataDir string
	// File is the SQLite database file, empty on PostgreSQL.
	File string
}

// CheckSecretFormat is the check the start does for every process. An empty
// DB_ENCRYPTION_SECRET passes, because version and health need none; a value
// that is set must be 64 hex characters. The error never repeats the value.
func CheckSecretFormat(cfg config.Config) error {
	raw := cfg.GetString(secretKey)
	if raw == "" {
		return nil
	}
	_, err := parseSecret(raw)
	return err
}

// CheckConnection refuses a DB_CONNECTION that names no engine. The other
// connections in config/database.go (the read-only and analytics ones) are
// derived from it and are not valid values.
func CheckConnection(cfg config.Config) error {
	_, err := engine(cfg)
	return err
}

func engine(cfg config.Config) (string, error) {
	switch e := cfg.GetString(defaultKey, EngineSQLite); e {
	case EngineSQLite, EnginePostgres:
		return e, nil
	}
	return "", errEngine
}

// LoadDatabase reads what a command that opens the database needs. The secret
// is optional here: it is parsed when set, and left empty otherwise.
func LoadDatabase(cfg config.Config) (*Store, error) {
	e, err := engine(cfg)
	if err != nil {
		return nil, err
	}
	dir, err := ResolveDataDir(cfg.GetString(dataDirKey))
	if err != nil {
		return nil, err
	}
	s := &Store{Engine: e, DataDir: dir}
	s.RetentionDays = number(cfg, retentionKey, "ANALYTICS_RETENTION_DAYS", minRetention, maxRetention, 90, &s.Notices)
	if e == EnginePostgres {
		s.PGSchema = cfg.GetString(pgSchemaKey)
	} else {
		s.File = cfg.GetString(sqliteFileKey)
	}
	if raw := cfg.GetString(secretKey); raw != "" {
		if s.Secret, err = parseSecret(raw); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// RequireSecret fails when no secret was set. The server calls it when it opens
// the database, not when it loads the settings, so version and health work
// without one.
func (s *Store) RequireSecret() error {
	if len(s.Secret) == 0 {
		return errSecretRequired
	}
	return nil
}

func parseSecret(raw string) ([]byte, error) {
	if len(raw) != secretHexLen {
		return nil, errSecretFormat
	}
	b, err := hex.DecodeString(raw)
	if err != nil || len(b) != secretByteLen {
		return nil, errSecretFormat
	}
	return b, nil
}
