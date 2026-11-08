package bootstrap

import (
	"github.com/goravel/framework/cache"
	"github.com/goravel/framework/contracts/foundation"
	"github.com/goravel/framework/database"
	"github.com/goravel/framework/filesystem"
	"github.com/goravel/framework/hash"
	"github.com/goravel/framework/http"
	"github.com/goravel/framework/log"
	"github.com/goravel/framework/route"
	"github.com/goravel/framework/schedule"
	"github.com/goravel/framework/session"
	"github.com/goravel/framework/validation"
	"github.com/goravel/framework/view"
	"github.com/goravel/gin"
	"github.com/goravel/postgres"
	"github.com/goravel/redis"
	"github.com/goravel/sqlite"

	"rllmx/app/providers"
)

// Providers returns the service providers in registration order. The data
// provider comes before the server's: the server waits for the data gate. The
// commands provider comes last of the framework-facing ones, so every
// registration it must filter has a console to filter.
func Providers(version string) func() []foundation.ServiceProvider {
	return func() []foundation.ServiceProvider {
		return []foundation.ServiceProvider{
			&log.ServiceProvider{},
			&cache.ServiceProvider{},
			&hash.ServiceProvider{},
			&http.ServiceProvider{},
			&session.ServiceProvider{},
			&filesystem.ServiceProvider{},
			&validation.ServiceProvider{},
			&view.ServiceProvider{},
			&route.ServiceProvider{},
			&gin.ServiceProvider{},
			&schedule.ServiceProvider{},
			&database.ServiceProvider{},
			&sqlite.ServiceProvider{},
			&postgres.ServiceProvider{},
			&redis.ServiceProvider{},
			&providers.CommandsServiceProvider{Allowlist: CommandsAllowlist()},
			&providers.CacheServiceProvider{},
			&providers.DataServiceProvider{Version: version},
			&providers.ServerServiceProvider{},
		}
	}
}
