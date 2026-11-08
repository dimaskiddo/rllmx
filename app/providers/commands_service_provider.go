package providers

import (
	"github.com/goravel/framework/console"
	"github.com/goravel/framework/contracts/binding"
	consolecontract "github.com/goravel/framework/contracts/console"
	"github.com/goravel/framework/contracts/foundation"
)

// CommandsServiceProvider applies the console allow-list to every provider's
// command registration, not just the framework's own batches. The allow-list
// was a partial gate: the database provider registered migrate, db:wipe and
// their neighbours with a bare artisan.Register that never consulted it, so
// those commands stayed reachable and could drop the 9router tables.
type CommandsServiceProvider struct {
	// Allowlist is the positive list of command signatures to keep. A nil list
	// keeps every command, as the framework's own filter defines it.
	Allowlist []string
}

// Relationship names what the provider reads. It binds nothing new: it
// replaces the container's Artisan instance, so declaring the binding would
// take the node away from the console provider that owns it.
func (r *CommandsServiceProvider) Relationship() binding.Relationship {
	return binding.Relationship{
		Bindings:     []string{},
		Dependencies: []string{binding.Artisan},
		ProvideFor:   []string{},
	}
}

// Register wraps the bound Artisan. It runs in the register phase, which the
// framework completes for every provider before any Boot, so a provider that
// registers commands from Boot already sees the wrapper.
func (r *CommandsServiceProvider) Register(app foundation.Application) {
	inner := app.MakeArtisan()
	if inner == nil {
		return
	}
	if _, wrapped := inner.(*filteredArtisan); wrapped {
		return
	}
	app.Instance(binding.Artisan, &filteredArtisan{Artisan: inner, keep: r.Allowlist})
	// The console provider's instance is cached; without this the cached one
	// would answer every later MakeArtisan and no registration would be filtered.
	app.Fresh(binding.Artisan)
}

// Boot has nothing to start.
func (r *CommandsServiceProvider) Boot(app foundation.Application) {}

// filteredArtisan keeps the framework's Artisan and filters what reaches it, so
// the matching rules stay the framework's own.
type filteredArtisan struct {
	consolecontract.Artisan
	keep []string
}

func (r *filteredArtisan) Register(commands []consolecontract.Command) {
	r.Artisan.Register(console.FilterCommandsByAllowlist(commands, r.keep))
}
