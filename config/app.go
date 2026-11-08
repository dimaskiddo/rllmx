package config

import (
	"os"

	contractsconfig "github.com/goravel/framework/contracts/config"
	"github.com/goravel/framework/support"
	"github.com/goravel/framework/support/carbon"
	"github.com/goravel/framework/support/color"
	"github.com/goravel/framework/support/path"

	"rllmx/app/facades"
	"rllmx/app/settings"
)

// Boot is the WithConfig hook. It runs after the framework has read the .env
// file and before any provider, and stops the start on a .env entry the code
// does not read or a setting out of range, then makes the data folder (see
// prepare). Goravel's own .env errors exit 0; these exit 1.
func Boot() {
	cfg := facades.Config()
	err := check(support.EnvFilePath, cfg)
	if err == nil {
		dir := cfg.GetString("app.data_dir")
		err = prepare(dir, databaseFile(cfg.GetString("database.default"), dir))
	}
	if err != nil {
		color.Errorln(err.Error())
		os.Exit(1)
	}
}

// check is what Boot refuses to start on: a .env entry the code does not read,
// a server, Redis or secret setting that is malformed, or a data directory that
// cannot be resolved. A missing DB_ENCRYPTION_SECRET passes here: only opening
// the store requires it.
func check(envFile string, cfg contractsconfig.Config) error {
	if err := settings.CheckEnvFile(envFile); err != nil {
		return err
	}
	if _, err := settings.LoadServer(cfg); err != nil {
		return err
	}
	if _, err := settings.LoadCache(cfg); err != nil {
		return err
	}
	if err := settings.CheckSecretFormat(cfg); err != nil {
		return err
	}
	if _, err := settings.LoadUpstream(cfg); err != nil {
		return err
	}
	if err := settings.CheckConnection(cfg); err != nil {
		return err
	}
	_, err := settings.ResolveDataDir(cfg.GetString("app.data_dir"))
	return err
}

// dataDir is DATA_DIR as an absolute path, or storage/app when it is empty.
// A value that cannot be resolved is left as it is: check refuses it at start,
// before anything reads it.
func dataDir() string {
	raw := envPath("DATA_DIR", path.Storage("app"))
	if abs, err := settings.ResolveDataDir(raw); err == nil {
		return abs
	}
	return raw
}

func init() {
	config := facades.Config()
	config.Add("app", map[string]any{
		// Application Name
		//
		// This value is the name of your application. This value is used when the
		// framework needs to place the application's name in a notification or
		// any other location as required by the application or its packages.
		"name": config.Env("APP_NAME", "Router-LLM"),

		// Data Directory
		//
		// Databases, identity files, backups and skills live here. It follows
		// the framework's storage layout, so logs sit beside it in storage/logs.
		"data_dir": dataDir(),

		// Application Environment
		//
		// This value determines the "environment" your application is currently
		// running in. This may determine how you prefer to configure various
		// services the application utilizes. Set this in your ".env" file.
		"env": config.Env("APP_ENV", "production"),

		// Application Debug Mode
		"debug": config.Env("APP_DEBUG", false),

		// Application Timezone
		//
		// Here you may specify the default timezone for your application.
		// Example: UTC, Asia/Shanghai
		// More: https://en.wikipedia.org/wiki/List_of_tz_database_time_zones
		"timezone": carbon.UTC,

		// Application Locale Configuration
		//
		// The application locale determines the default locale that will be used
		// by the translation service provider. You are free to set this value
		// to any of the locales which will be supported by the application.
		"locale": "en",

		// Application Fallback Locale
		//
		// The fallback locale determines the locale to use when the current one
		// is not available. You may change the value to correspond to any of
		// the language folders that are provided through your application.
		"fallback_locale": "en",

		// Encryption Key
		//
		// 32 character string, otherwise these encrypted strings
		// will not be safe. Please do this before deploying an application!
		"key": config.Env("APP_KEY", ""),

		// Disabled Runners
		//
		// The framework's HTTP runner builds its server without a header or idle
		// timeout and has no setting for either. rllmx:http (app/http/server.go)
		// serves the router with the HTTP_SERVER_* values instead.
		"disabled_runners": []string{"goravel:http"},

		"maintenance": map[string]any{
			// Maintenance Mode Driver
			//
			// These configuration options determine the driver used to determine and
			// manage Goravel's "maintenance mode" status. The "cache" driver will
			// allow maintenance mode to be controlled across multiple machines.
			//
			// Supported drivers: "file", "cache"
			"driver": "file",
			// Maintenance Mode Store
			//
			// This option controls the cache store used by the "cache" maintenance
			// driver. Leave it empty to use the default cache store.
			"store": "",
		},
	})
}
