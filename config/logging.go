package config

import (
	"github.com/goravel/framework/contracts/log"
	"github.com/goravel/framework/foundation/json"
	"github.com/goravel/framework/log/logger"

	"rllmx/app/facades"
	"rllmx/app/platform/logbook"
	"rllmx/app/platform/redactor"
)

// logFile is relative to the project root: the single and daily drivers join
// it to that root themselves, so an absolute path would be nested inside it.
const logFile = "storage/logs/rllmx.log"

// console is the ring the dashboard's Console Log reads. The channel is built
// at init, before any provider registers, so the ring is owned here and reached
// through ConsoleLog by the route wiring.
var console = logbook.New(logbook.Size)

// ConsoleLog returns the ring every clean log entry also lands in.
func ConsoleLog() *logbook.Book { return console }

// logDriver is the file driver behind the redactor, with the console ring as a
// second reader. Order matters: redaction runs first, so the ring never holds a
// secret.
func logDriver(inner log.Logger) *redactor.Channel {
	ch := redactor.NewChannel(inner)
	ch.Tee(func(e log.Entry) { console.Add(e.Level().String(), e.Time(), e.Message()) })
	return ch
}

func init() {
	config := facades.Config()
	config.Add("logging", map[string]any{
		// Default Log Channel
		//
		// This option defines the default log channel that gets used when writing
		// messages to the logs. The name specified in this option should match
		// one of the channels defined in the "channels" configuration array.
		"default": config.Env("LOG_CHANNEL", "single"),

		// Log Channels
		//
		// Here you may configure the log channels for your application.
		// Available Level: "debug", "info", "warning", "error", "fatal", "panic"
		// Available Formatter: "text", "json"
		//
		// "single" and "daily" are custom drivers: the framework's own file
		// handler, behind the redactor, so no secret, key, cookie or password
		// reaches a file whatever the caller logged. The keys under each channel
		// (path, level, formatter, days) are the ones the framework reads. The
		// framework's "print" flag is not available on a custom driver, so
		// nothing is copied to the console.
		"channels": map[string]any{
			"stack": map[string]any{
				"driver":   "stack",
				"channels": []string{"daily"},
			},
			"single": map[string]any{
				"driver":    "custom",
				"via":       logDriver(logger.NewSingle(config, json.New())),
				"path":      logFile,
				"level":     config.Env("LOG_LEVEL", "info"),
				"formatter": "text",
			},
			"daily": map[string]any{
				"driver":    "custom",
				"via":       logDriver(logger.NewDaily(config, json.New())),
				"path":      logFile,
				"level":     config.Env("LOG_LEVEL", "info"),
				"days":      7,
				"formatter": "text",
			},
		},
	})
}
