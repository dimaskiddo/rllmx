package translate_test

// The formats live in sub-packages, so a test binary that exercises the
// registry imports the ones that have moved. An external test package shares
// the binary with the package's own tests, and its init() runs before them.
// Each move adds one line here.
import (
	_ "rllmx/app/llm/translate/claude"
	_ "rllmx/app/llm/translate/commandcode"
	_ "rllmx/app/llm/translate/cursor"
	_ "rllmx/app/llm/translate/gemini"
	_ "rllmx/app/llm/translate/kiro"
	_ "rllmx/app/llm/translate/ollama"
	_ "rllmx/app/llm/translate/openai"
	_ "rllmx/app/llm/translate/responses"
)
