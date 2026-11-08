package commands

import (
	"github.com/goravel/framework/contracts/console"
	"github.com/goravel/framework/contracts/console/command"
)

// Version prints the version and commit the binary was built with.
type Version struct {
	Version string
	Commit  string
}

// Signature The name and signature of the console command.
func (r *Version) Signature() string {
	return "version"
}

// Description The console command description.
func (r *Version) Description() string {
	return "Print the version and commit hash"
}

// Extend The console command extend.
func (r *Version) Extend() command.Extend {
	return command.Extend{}
}

// Handle Execute the console command.
func (r *Version) Handle(ctx console.Context) error {
	ctx.Line("rllmx " + r.Version + " (" + r.Commit + ")")
	return nil
}
