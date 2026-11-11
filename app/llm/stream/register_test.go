package stream_test

// The formats live in sub-packages; a test binary that plays an upstream's
// events through the registry imports the ones that have moved.
import (
	_ "rllmx/app/llm/translate/claude"
	_ "rllmx/app/llm/translate/gemini"
	_ "rllmx/app/llm/translate/ollama"
	_ "rllmx/app/llm/translate/openai"
	_ "rllmx/app/llm/translate/responses"
)
