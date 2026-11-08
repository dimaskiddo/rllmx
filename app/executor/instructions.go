package executor

import (
	"embed"
	"strings"
)

// instructionFiles holds the default instruction text of each backend that
// refuses a request without one, one file per backend. The files are our own
// wording.
//
//go:embed instructions/*.txt
var instructionFiles embed.FS

// instruction returns the trimmed text of instructions/<name>.txt. A name with
// no file is a mistake in the code, caught by the test that reads every name the
// executors use.
func instruction(name string) string {
	raw, err := instructionFiles.ReadFile("instructions/" + name + ".txt")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}
