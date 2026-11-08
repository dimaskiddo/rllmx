package config

import (
	"rllmx/app/facades"
)

func init() {
	config := facades.Config()
	config.Add("stream", map[string]any{
		// How long to wait for the first event of a streamed answer: the time a
		// model needs to read a long prompt.
		"first_chunk_timeout": config.Env("STREAM_FIRST_CHUNK_TIMEOUT", "200s"),
		// How long a stream may stay silent before it is cut.
		"stall_timeout": config.Env("STREAM_STALL_TIMEOUT", "360s"),
		// How long a finished turn waits for the usage report some upstreams
		// send after it, before the stream closes without it.
		"usage_wait": config.Env("STREAM_USAGE_WAIT", "3s"),
	})
}
