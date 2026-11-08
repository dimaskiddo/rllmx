package providers

import (
	"slices"
	"testing"

	"github.com/goravel/framework/contracts/binding"
	consolecontract "github.com/goravel/framework/contracts/console"
	"github.com/goravel/framework/contracts/console/command"
)

// stubCommand carries a signature and nothing else; the wrapper only reads it.
type stubCommand struct{ sig string }

func (c stubCommand) Signature() string                    { return c.sig }
func (c stubCommand) Description() string                  { return "" }
func (c stubCommand) Extend() command.Extend               { return command.Extend{} }
func (c stubCommand) Handle(consolecontract.Context) error { return nil }

// recordingArtisan is the inner console: it keeps every signature it was given.
type recordingArtisan struct{ got []string }

func (r *recordingArtisan) Register(commands []consolecontract.Command) {
	for _, c := range commands {
		r.got = append(r.got, c.Signature())
	}
}
func (r *recordingArtisan) Call(string) error        { return nil }
func (r *recordingArtisan) CallAndExit(string)       {}
func (r *recordingArtisan) Run([]string, bool) error { return nil }

func commands(sigs ...string) []consolecontract.Command {
	out := make([]consolecontract.Command, len(sigs))
	for i, s := range sigs {
		out[i] = stubCommand{s}
	}
	return out
}

func TestTheWrapperDropsCommandsOutsideTheAllowlist(t *testing.T) {
	inner := &recordingArtisan{}
	wrapped := &filteredArtisan{Artisan: inner, keep: []string{"health", "route:list", "make:*"}}
	wrapped.Register(commands(
		"migrate", "migrate:fresh", "migrate:reset", "migrate:rollback", "migrate:refresh",
		"db:wipe", "db:seed", "db:show", "db:table", "health", "route:list",
	))
	want := []string{"health", "route:list"}
	if !slices.Equal(inner.got, want) {
		t.Fatalf("the inner console got %v, want %v", inner.got, want)
	}
}

func TestTheWrapperKeepsTheGlobEntries(t *testing.T) {
	inner := &recordingArtisan{}
	wrapped := &filteredArtisan{Artisan: inner, keep: []string{"make:*"}}
	wrapped.Register(commands("make:command", "make:migration", "make:model"))
	if len(inner.got) != 3 {
		t.Fatalf("the make:* entries were dropped: %v", inner.got)
	}
}

func TestANilAllowlistKeepsEverything(t *testing.T) {
	inner := &recordingArtisan{}
	wrapped := &filteredArtisan{Artisan: inner}
	wrapped.Register(commands("db:wipe", "migrate"))
	if len(inner.got) != 2 {
		t.Fatalf("a nil allowlist must keep every command, as the framework's filter does: %v", inner.got)
	}
}

func TestTheProviderReplacesTheBoundArtisan(t *testing.T) {
	inner := &recordingArtisan{}
	app := consoleApp(inner)
	(&CommandsServiceProvider{Allowlist: []string{"health"}}).Register(app)

	got := app.MakeArtisan()
	wrapped, ok := got.(*filteredArtisan)
	if !ok {
		t.Fatalf("the container still answers %T", got)
	}
	if wrapped.Artisan != inner {
		t.Fatal("the wrapper does not hold the framework's console")
	}
	wrapped.Register(commands("db:wipe", "health"))
	if !slices.Equal(inner.got, []string{"health"}) {
		t.Fatalf("the inner console got %v", inner.got)
	}
}

func TestTheProviderWrapsOnlyOnce(t *testing.T) {
	inner := &recordingArtisan{}
	app := consoleApp(inner)
	p := &CommandsServiceProvider{Allowlist: []string{"health"}}
	p.Register(app)
	p.Register(app)

	wrapped, ok := app.MakeArtisan().(*filteredArtisan)
	if !ok {
		t.Fatal("the container lost the wrapper")
	}
	if _, doubled := wrapped.Artisan.(*filteredArtisan); doubled {
		t.Fatal("a second Register wrapped the wrapper again")
	}
}

func TestTheProviderDoesNothingWithoutAnArtisan(t *testing.T) {
	app := &fakeApp{}
	(&CommandsServiceProvider{}).Register(app)
	if got := app.MakeArtisan(); got != nil {
		t.Fatalf("the container got %T", got)
	}
}

func TestTheProviderDependsOnTheConsole(t *testing.T) {
	rel := (&CommandsServiceProvider{}).Relationship()
	if !slices.Contains(rel.Dependencies, binding.Artisan) {
		t.Fatalf("dependencies = %v", rel.Dependencies)
	}
	if len(rel.Bindings) != 0 {
		t.Fatalf("the provider binds nothing new, but claims %v", rel.Bindings)
	}
}

func TestTheCommandsProviderBootDoesNotTouchTheContainer(t *testing.T) {
	(&CommandsServiceProvider{}).Boot(&fakeApp{})
}
