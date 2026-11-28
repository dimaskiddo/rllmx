package config

import (
	contractsconfig "github.com/goravel/framework/contracts/config"
	"github.com/goravel/framework/contracts/foundation"
	"github.com/goravel/framework/contracts/log"
	"github.com/goravel/framework/foundation/json"
	"github.com/goravel/framework/log/logger"

	"rllmx/app/facades"
	"rllmx/app/platform/logbook"
	"rllmx/app/platform/redactor"
	"rllmx/app/platform/settings"
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

// stdout is a custom driver writing the framework's own text or json line to
// the standard output, for a terminal or a container's log collector.
type stdout struct {
	config contractsconfig.Config
	json   foundation.Json
}

func (s stdout) Handle(channel string) (log.Handler, error) {
	level := logger.GetLevelFromString(s.config.GetString(channel + ".level"))
	formatter := s.config.GetString(channel+".formatter", logger.FormatterText)
	return logger.NewConsoleHandler(s.config, s.json, level, formatter), nil
}

// logOutput reads LOG_OUTPUT. An unknown value keeps both sinks here; Boot
// refuses it before anything runs.
func logOutput(cfg contractsconfig.Config) settings.LogOutput {
	both := settings.LogOutput{File: true, Stdout: true}
	raw, ok := cfg.Env("LOG_OUTPUT", "both").(string)
	if !ok {
		return both
	}
	output, err := settings.ParseLogOutput(raw)
	if err != nil {
		return both
	}
	return output
}

// logSinks is the drivers one channel writes to: the file driver when the
// output includes it, and the console driver when it includes the console.
func logSinks(output settings.LogOutput, cfg contractsconfig.Config, file log.Logger) []log.Logger {
	sinks := make([]log.Logger, 0, 2)
	if output.File {
		sinks = append(sinks, file)
	}
	if output.Stdout {
		sinks = append(sinks, stdout{config: cfg, json: json.New()})
	}
	return sinks
}

// logLevel reads LOG_LEVEL once for the channels. An unreadable value uses info:
// the channels are built before Boot can report it, and debug, the framework's
// own fallback, is the loudest setting.
func logLevel(cfg contractsconfig.Config) string {
	raw, _ := cfg.Env("LOG_LEVEL", "info").(string)
	level, err := settings.ParseLogLevel(raw)
	if err != nil {
		return "info"
	}
	return level
}

// logDriver puts the redactor in front of the drivers, with the console ring as
// a second reader. Order matters: redaction runs first, so no sink and no ring
// ever holds a secret.
func logDriver(inners ...log.Logger) *redactor.Channel {
	ch := redactor.NewChannel(inners...)
	ch.Tee(func(e log.Entry) { console.Add(e.Level().String(), e.Time(), e.Message()) })
	return ch
}

func init() {
	config := facades.Config()
	output := logOutput(config)
	config.Add("logging", map[string]any{
		// Default Log Channel
		//
		// This option defines the default log channel that gets used when writing
		// messages to the logs. The name specified in this option should match
		// one of the channels defined in the "channels" configuration array.
		"default": config.Env("LOG_CHANNEL", "single"),

		// Log Output
		//
		// Which sinks a channel writes to: the file, the standard output or
		// both. Boot refuses an unknown value.
		"output": config.Env("LOG_OUTPUT", "both"),
		// The value as written, so a typo can be reported; the channels carry the
		// cleaned one.
		"level": config.Env("LOG_LEVEL", "info"),

		// Log Channels
		//
		// Here you may configure the log channels for your application.
		// Available Level: "debug", "info", "warning", "error", "fatal", "panic"
		// Available Formatter: "text", "json"
		//
		// "single" and "daily" are custom drivers: the framework's own file
		// handler, behind the redactor, so no secret, key, cookie or password
		// reaches a file whatever the caller logged. The keys under each channel
		// (path, level, formatter, days) are the ones the framework reads.
		"channels": map[string]any{
			"stack": map[string]any{
				"driver":   "stack",
				"channels": []string{"daily"},
			},
			"single": map[string]any{
				"driver":    "custom",
				"via":       logDriver(logSinks(output, config, logger.NewSingle(config, json.New()))...),
				"path":      logFile,
				"level":     logLevel(config),
				"formatter": "text",
			},
			"daily": map[string]any{
				"driver":    "custom",
				"via":       logDriver(logSinks(output, config, logger.NewDaily(config, json.New()))...),
				"path":      logFile,
				"level":     logLevel(config),
				"days":      7,
				"formatter": "text",
			},
		},
	})
}
