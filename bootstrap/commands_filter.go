package bootstrap

// CommandsAllowlist is the positive list WithCommandsFilter keeps. The
// framework registers migrate:fresh, migrate:reset, migrate:rollback,
// migrate:refresh, db:wipe and db:seed, which would drop or rewrite the 9router
// tables, and migrate prints its error and exits 0, so none of those are
// listed. db:show and db:table print rows. The filter also gates our own
// commands, so each one is listed here. The generators are listed one by one,
// not as make:*: make:model, make:observer, make:seeder and make:factory
// scaffold the ORM, which this program does not use, so they are left out and a
// generator a later framework release adds is dropped until it is listed.
func CommandsAllowlist() []string {
	return []string{
		"list", "about", "help", "route:list", "schedule:list", "schedule:run", "key:generate",
		"package:install", "package:uninstall", "build", "vendor:publish", "env:encrypt", "env:decrypt", "up", "down",
		"make:command", "make:controller", "make:filter", "make:middleware", "make:migration", "make:package",
		"make:provider", "make:request", "make:rule", "make:test", "make:view",
		"health", "version",
		"db:backup", "db:check", "db:migrate", "db:vacuum", "db:import-analytics", "db:import-config", "db:export-plain",
		"keys:create", "keys:list", "keys:revoke {id}", "password:reset", "auth:mode",
		"skills:sync",
	}
}
