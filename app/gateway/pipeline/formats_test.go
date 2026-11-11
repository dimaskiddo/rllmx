package pipeline

import (
	"slices"
	"testing"

	"rllmx/app/llm/translate"
)

// A codec registers itself in init(), so a package nobody imports is a format
// nobody can use. The pipeline imports the upstream-only formats; this fails the
// day one of those imports is lost.
func TestEveryFormatAnUpstreamSpeaksIsRegistered(t *testing.T) {
	have := translate.Formats()
	for _, want := range []string{
		translate.FormatClaude, translate.FormatOpenAI, translate.FormatResponses, translate.FormatGemini,
		translate.FormatOllama, translate.FormatCloudCode, translate.FormatVertex,
		translate.FormatKiro, translate.FormatCommandCode, translate.FormatCursor,
	} {
		if !slices.Contains(have, want) {
			t.Errorf("format %q has no codec; is its package imported?", want)
		}
	}
	// No client speaks an upstream-only format, so none may be offered as an
	// answer format.
	for _, f := range []string{translate.FormatKiro, translate.FormatCommandCode, translate.FormatCursor} {
		if slices.Contains(translate.WriteFormats(), f) {
			t.Errorf("%q is offered as a client format", f)
		}
	}
}
