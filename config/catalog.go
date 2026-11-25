package config

import (
	"rllmx/app/facades"
)

func init() {
	config := facades.Config()
	config.Add("catalog", map[string]any{
		// Whether the model catalog is refreshed from models.dev. false only
		// stops the timer: a file already on disk is still loaded at start.
		"sync": config.Env("CATALOG_SYNC", false),
	})
}
