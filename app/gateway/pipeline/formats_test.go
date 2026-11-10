package pipeline

import (
	"slices"
	"testing"

	"rllmx/app/llm/translate"
)

// A codec registers itself in init(), so a package nobody imports is a format
// nobody can use. The pipeline imports the two upstream-only formats; this
// fails the day that import is lost.
func TestEveryFormatAnUpstreamSpeaksIsRegistered(t *testing.T) {
	have := translate.Formats()
	for _, want := range []string{
		translate.FormatClaude, translate.FormatOpenAI, translate.FormatResponses, translate.FormatGemini,
		translate.FormatOllama, translate.FormatCloudCode, translate.FormatVertex,
		translate.FormatKiro, translate.FormatCommandCode,
	} {
		if !slices.Contains(have, want) {
			t.Errorf("format %q has no codec; is its package imported?", want)
		}
	}
	// No client speaks the two upstream-only formats, so neither may be offered
	// as an answer format.
	for _, f := range []string{translate.FormatKiro, translate.FormatCommandCode} {
		if slices.Contains(translate.WriteFormats(), f) {
			t.Errorf("%q is offered as a client format", f)
		}
	}
}
