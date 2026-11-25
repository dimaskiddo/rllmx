package main

import (
	// The distroless image carries no zoneinfo, and day rollups and log times
	// read the local zone.
	_ "time/tzdata"

	"rllmx/bootstrap"
)

// Set at build time with -ldflags -X.
var (
	version = "dev"
	commit  = "none"
)

func main() {
	app := bootstrap.Boot(version, commit)

	app.Start()
	bootstrap.Relaunch()
}
