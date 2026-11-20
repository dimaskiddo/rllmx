package skill

import (
	"embed"
	"io/fs"
)

// builtinFiles is the core set, compiled in so a fresh install routes without
// an import or a sync.
//
//go:embed builtin/*/*
var builtinFiles embed.FS

// Builtin is the embedded core registry.
func Builtin() fs.FS { return builtinFiles }
