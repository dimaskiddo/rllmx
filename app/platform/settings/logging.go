package settings

import (
	"fmt"
	"strings"

	"github.com/goravel/framework/contracts/config"
)

const (
	logOutputKey = "logging.output"
	// logLevelKey holds LOG_LEVEL as written; the channels hold the cleaned one.
	logLevelKey = "logging.level"
)

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

// CheckLogging reads LOG_OUTPUT and LOG_LEVEL. Both decide how much is written
// where, not where the program connects or whom it trusts, so an unreadable one
// keeps its default and is reported. The channels are built before this runs,
// so each reader falls back on its own too.
func CheckLogging(cfg config.Config, n *Notices) {
	if _, err := ParseLogOutput(cfg.GetString(logOutputKey, "both")); err != nil {
		n.warn("LOG_OUTPUT", "must be file, stdout or both", "both")
	}
	if _, err := ParseLogLevel(cfg.GetString(logLevelKey, "info")); err != nil {
		n.warn("LOG_LEVEL", "must be debug, info, warning or error", "info")
	}
}
