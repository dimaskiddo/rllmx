package commands

import (
	"github.com/goravel/framework/contracts/console"
	"github.com/goravel/framework/contracts/console/command"

	apphttp "rllmx/app/http"
	"rllmx/app/platform/settings"
)

// Health asks the local server's health route. It exits 0 only when the answer
// is 200 with {"ok":true}, so a container health check can use it.
type Health struct {
	// Settings loads the server settings, which name the address to ask.
	Settings func() (*settings.Server, error)
}

// Signature The name and signature of the console command.
func (r *Health) Signature() string {
	return "health"
}

// Description The console command description.
func (r *Health) Description() string {
	return "Check the local server's /api/health; the exit code reflects the result"
}

// Extend The console command extend.
func (r *Health) Extend() command.Extend {
	return command.Extend{}
}

// Handle Execute the console command.
func (r *Health) Handle(ctx console.Context) error {
	s, err := r.Settings()
	if err != nil {
		return err
	}
	if err := apphttp.Probe(ctx, apphttp.HealthTargetOf(s)); err != nil {
		return err
	}
	ctx.Line("ok")
	return nil
}
