package bootstrap

import (
	"path"
	"slices"
	"strings"
	"testing"

	"rllmx/app/providers"
)

func kept(sig string) bool {
	for _, entry := range CommandsAllowlist() {
		if entry == sig {
			return true
		}
		if ok, _ := path.Match(entry, sig); ok {
			return true
		}
	}
	return false
}

func TestTheFrameworkCommandsThatRewriteTheNineRouterTablesAreDropped(t *testing.T) {
	for _, sig := range []string{
		"migrate", "migrate:fresh", "migrate:reset", "migrate:rollback", "migrate:refresh", "migrate:status",
		"db:wipe", "db:seed", "db:show", "db:table",
	} {
		if kept(sig) {
			t.Errorf("%s is kept: it can drop, rewrite or print the 9router tables", sig)
		}
	}
}

func TestTheOrmGeneratorsAndUnlistedGeneratorsAreDropped(t *testing.T) {
	for _, sig := range []string{"make:model", "make:observer", "make:seeder", "make:factory", "make:somethingnew"} {
		if kept(sig) {
			t.Errorf("%s is kept: this program has no ORM, and a generator nobody listed is not wanted", sig)
		}
	}
}

func TestNoGeneratorIsKeptByAGlob(t *testing.T) {
	for _, e := range CommandsAllowlist() {
		if strings.HasPrefix(e, "make:") && strings.ContainsAny(e, "*?[") {
			t.Errorf("%s keeps every generator, the ORM ones included", e)
		}
	}
}

func TestTheCommandsTheProjectUsesAreKept(t *testing.T) {
	for _, sig := range []string{
		"health", "version", "list", "about", "route:list", "schedule:list", "schedule:run", "key:generate",
		"package:install", "package:uninstall", "build", "vendor:publish", "env:encrypt", "env:decrypt", "up", "down",
		"make:command", "make:controller", "make:filter", "make:middleware", "make:migration", "make:package",
		"make:provider", "make:request", "make:rule", "make:test", "make:view",
		"db:backup", "db:check", "db:vacuum", "db:import-analytics", "db:import-config", "db:export-plain",
		"keys:create", "keys:list", "keys:revoke {id}", "password:reset", "skills:sync",
	} {
		if !kept(sig) {
			t.Errorf("%s is dropped", sig)
		}
	}
}

func TestEveryCommandOfOursIsOnTheList(t *testing.T) {
	for _, c := range Commands("t", "c") {
		if !kept(c.Signature()) {
			t.Errorf("our command %s is not on the allow-list and would be dropped by the framework", c.Signature())
		}
	}
}

func TestTheCommandsProviderCarriesTheAllowlist(t *testing.T) {
	var found *providers.CommandsServiceProvider
	for _, p := range Providers("t")() {
		if c, ok := p.(*providers.CommandsServiceProvider); ok {
			found = c
		}
	}
	if found == nil {
		t.Fatal("Providers() does not install the provider that filters every registration")
	}
	if !slices.Equal(found.Allowlist, CommandsAllowlist()) {
		t.Fatalf("the provider filters by %v, the list is %v", found.Allowlist, CommandsAllowlist())
	}
	if len(found.Allowlist) == 0 {
		t.Fatal("an empty allowlist drops every command, our own included")
	}
}

func TestTheListHasNoDuplicatesAndNoEmptyEntry(t *testing.T) {
	list := CommandsAllowlist()
	seen := map[string]bool{}
	for _, e := range list {
		if e == "" {
			t.Fatal("an empty entry")
		}
		if seen[e] {
			t.Errorf("%s listed twice", e)
		}
		seen[e] = true
	}
	if !slices.Contains(list, "health") {
		t.Fatal("health missing")
	}
}
