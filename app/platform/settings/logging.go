package settings

import (
	"fmt"
	"strings"

	"github.com/goravel/framework/contracts/config"
)

const logOutputKey = "logging.output"

// LogOutput is where a log entry goes: the file, the standard output, or both.
type LogOutput struct {
	File   bool
	Stdout bool
}

// ParseLogOutput reads a LOG_OUTPUT value. Empty means both, the value a
// default install runs with.
func ParseLogOutput(v string) (LogOutput, error) {
	switch v {
	case "file":
		return LogOutput{File: true}, nil
	case "stdout":
		return LogOutput{Stdout: true}, nil
	case "", "both":
		return LogOutput{File: true, Stdout: true}, nil
	}
	return LogOutput{}, fmt.Errorf("settings: LOG_OUTPUT: must be file, stdout or both")
}

// logLevels are the names the framework accepts. A typo would otherwise fall
// back to debug, the loudest setting, so it stops the start instead.
var logLevels = map[string]bool{"debug": true, "info": true, "warning": true, "error": true}

// ParseLogLevel reads a LOG_LEVEL value. Empty means info.
func ParseLogLevel(v string) (string, error) {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return "info", nil
	}
	if !logLevels[v] {
		return "", fmt.Errorf("settings: LOG_LEVEL: must be debug, info, warning or error")
	}
	return v, nil
}

// CheckLogging refuses a LOG_OUTPUT that names no sink or a LOG_LEVEL the
// framework would silently read as debug. The channels are built before this
// runs, so a bad value keeps its default until the start stops.
func CheckLogging(cfg config.Config) error {
	if _, err := ParseLogOutput(cfg.GetString(logOutputKey, "both")); err != nil {
		return err
	}
	_, err := ParseLogLevel(cfg.GetString("logging.channels.single.level", "info"))
	return err
}
