package config

import (
	"github.com/goravel/framework/foundation/json"
	"github.com/goravel/framework/log/logger"

	"rllmx/app/facades"
	"rllmx/app/platform/redactor"
)

// logFile is relative to the project root: the single and daily drivers join
// it to that root themselves, so an absolute path would be nested inside it.
const logFile = "storage/logs/rllmx.log"

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
				"via":       redactor.NewChannel(logger.NewSingle(config, json.New())),
				"path":      logFile,
				"level":     config.Env("LOG_LEVEL", "info"),
				"formatter": "text",
			},
			"daily": map[string]any{
				"driver":    "custom",
				"via":       redactor.NewChannel(logger.NewDaily(config, json.New())),
				"path":      logFile,
				"level":     config.Env("LOG_LEVEL", "info"),
				"days":      7,
				"formatter": "text",
			},
		},
	})
}
