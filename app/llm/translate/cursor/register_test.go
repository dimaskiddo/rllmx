package cursor_test

// The cursor codec is written from the hub, and its test reads back through the
// OpenAI codec, so this binary imports it.
import _ "rllmx/app/llm/translate/openai"
