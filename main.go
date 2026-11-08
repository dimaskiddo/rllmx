package main

import (
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
}
