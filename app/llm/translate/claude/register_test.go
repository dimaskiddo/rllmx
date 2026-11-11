package claude_test

// The OpenAI codec is asked for through the registry by a test that crosses
// formats, so this binary imports it.
import _ "rllmx/app/llm/translate/openai"
