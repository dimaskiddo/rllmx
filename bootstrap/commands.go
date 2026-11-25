package bootstrap

import (
	contractsdb "github.com/goravel/framework/contracts/database/db"
	contractsschema "github.com/goravel/framework/contracts/database/schema"
	frameworkhash "github.com/goravel/framework/contracts/hash"

	"github.com/goravel/framework/contracts/console"

	"rllmx/app/console/commands"
	"rllmx/app/facades"
	"rllmx/app/platform/settings"
	"rllmx/app/providers"
)

// Commands lists the Artisan commands. version and commit come from main.
func Commands(version, commit string) []console.Command {
	db := commands.NewDatabaseRuntime(func() commands.Facades {
		return commands.Facades{
			Config: facades.Config(), Log: facades.Log(),
			Connect: func(name string) (contractsdb.DB, contractsschema.Schema) {
				return facades.DB().Connection(name), facades.Schema().Connection(name)
			},
			Schema: func() contractsschema.Schema { return facades.Schema() },
			Opener: providers.SQLiteOpener(facades.Config(), facades.Log()),
		}
	}, version)
	ar := commands.NewAuthRuntime(db, func() frameworkhash.Hash { return facades.App().MakeHash() })
	return []console.Command{
		&commands.Health{Settings: func() (*settings.Server, error) { return settings.LoadServer(facades.Config()) }},
		&commands.Version{Version: version, Commit: commit},
		&commands.Upgrade{Version: version, Commit: commit, Fetch: guardedFetch(version, "application/octet-stream", 64<<20)},
		&commands.DBBackup{R: db},
		&commands.DBCheck{R: db},
		&commands.DBMigrate{R: db},
		&commands.DBVacuum{R: db},
		&commands.DBImportAnalytics{R: db},
		&commands.AuthMode{R: db},
		&commands.DBImportConfig{R: db},
		&commands.SkillsSync{R: db},
		&commands.KeysCreate{R: ar},
		&commands.KeysList{R: ar},
		&commands.KeysRevoke{R: ar},
		&commands.PasswordReset{R: ar},
		&commands.DBExportPlain{R: ar},
	}
}
