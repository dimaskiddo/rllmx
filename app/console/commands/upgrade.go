package commands

import (
	"github.com/goravel/framework/contracts/console"
	"github.com/goravel/framework/contracts/console/command"

	"rllmx/app/platform/upgrade"
)

// Upgrade replaces this binary with the newest release for this platform and
// restarts it. The download is verified against the release's checksums before
// anything on disk changes.
type Upgrade struct {
	Version string
	Commit  string
	// Fetch reads one release address through the guarded client.
	Fetch upgrade.Fetcher
}

func (c *Upgrade) Signature() string { return "upgrade" }

func (c *Upgrade) Description() string {
	return "Replace this binary with the newest release and restart it"
}

func (c *Upgrade) Extend() command.Extend {
	return command.Extend{Flags: []command.Flag{
		&command.BoolFlag{Name: "check", Usage: "only report whether a newer release exists"},
		&command.BoolFlag{Name: "yes", Usage: "do not ask for confirmation"},
	}}
}

func (c *Upgrade) Handle(ctx console.Context) error {
	opts := upgrade.Options{Version: c.Version, Fetch: c.Fetch}
	latest, err := upgrade.Check(ctx, opts)
	if err != nil {
		return err
	}
	if latest == upgrade.Version(c.Version) {
		ctx.Line("rllmx " + upgrade.Version(c.Version) + " is the newest release")
		return nil
	}
	ctx.Line("rllmx " + upgrade.Version(c.Version) + " -> " + latest)
	if ctx.OptionBool("check") {
		return nil
	}
	if !ctx.OptionBool("yes") && !Confirmed(ctx, "Replace this binary and restart it? [y/N]") {
		return nil
	}
	res, err := upgrade.Install(ctx, opts)
	if err != nil {
		return err
	}
	ctx.Line("replaced " + res.Exe + "; restarting")
	return upgrade.Restart(res.Exe)
}
