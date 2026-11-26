package main

import (
	"os"
	"runtime"

	"github.com/goravel/framework/support/color"

	"rllmx/bootstrap"
)

// Set at build time with -ldflags -X.
var (
	version = "dev"
	commit  = "none"
)

func main() {
	// A start that cannot go on reports one line and stops, whether it was a
	// setting, a store or a runner that refused. Anything that is not a plain
	// error is a bug in this program and keeps its stack.
	defer func() {
		switch r := recover().(type) {
		case nil:
		case runtime.Error:
			panic(r)
		case error:
			color.Errorln(r.Error())
			os.Exit(1)
		case string:
			color.Errorln(r)
			os.Exit(1)
		default:
			panic(r)
		}
	}()

	app := bootstrap.Boot(version, commit)

	app.Start()
	bootstrap.Relaunch()
}
