// Package web holds the dashboard's static files, embedded in the binary.
package web

import "embed"

// Static is the folder served as /static, plus the pages the router names.
//
//go:embed static
var Static embed.FS
