package settings

import (
	"fmt"

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

// CheckLogging refuses a LOG_OUTPUT that names no sink. The channels are built
// before this runs, so an unknown value keeps both sinks until the start stops.
func CheckLogging(cfg config.Config) error {
	_, err := ParseLogOutput(cfg.GetString(logOutputKey, "both"))
	return err
}
