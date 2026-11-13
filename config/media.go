package config

import (
	"rllmx/app/facades"
)

func init() {
	config := facades.Config()
	config.Add("media", map[string]any{
		// How long one video create or poll may take to answer. The job itself
		// runs upstream; this bounds the HTTP round trip.
		"video_timeout": config.Env("MEDIA_VIDEO_TIMEOUT", "120s"),
		// The SearXNG instance this router searches through. Empty serves no
		// SearXNG: its address is never taken from a client.
		"searxng_url": config.Env("MEDIA_SEARXNG_URL", ""),
	})
}
