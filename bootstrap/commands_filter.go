package bootstrap

// CommandsAllowlist is the positive list WithCommandsFilter keeps. The
// framework registers migrate:fresh, migrate:reset, migrate:rollback,
// migrate:refresh, db:wipe and db:seed, which would drop or rewrite the 9router
// tables, and migrate prints its error and exits 0, so none of those are
// listed. db:show and db:table print rows. The filter also gates our own
// commands, so each one is listed here.
func CommandsAllowlist() []string {
	return []string{
		"list", "about", "help", "route:list", "schedule:list", "schedule:run", "key:generate",
		"package:install", "package:uninstall", "build", "vendor:publish", "env:encrypt", "env:decrypt", "up", "down",
		"make:*",
		"health", "version",
		"db:backup", "db:check", "db:vacuum", "db:import-analytics", "db:import-config", "db:export-plain",
		"keys:create", "keys:list", "keys:revoke {id}", "password:reset",
	}
}
