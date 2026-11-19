package config

import (
	"rllmx/app/facades"
)

func init() {
	config := facades.Config()
	config.Add("saver", map[string]any{
		// The Headroom sidecar's address. It is only a default: the settings
		// row's headroomUrl wins, and the router never starts the sidecar.
		"headroom_url": config.Env("SAVER_HEADROOM_URL", ""),
	})
}
