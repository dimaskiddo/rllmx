package config

import (
	"rllmx/app/facades"
)

func init() {
	config := facades.Config()
	config.Add("hermes", map[string]any{
		// The agent's own directory, holding memories/MEMORY.md and USER.md.
		// Empty leaves the memory skill reading nothing.
		"home": config.Env("HERMES_HOME", ""),
	})
}
