package routing

import (
	"regexp"
	"strings"
)

type inferRule struct {
	re       *regexp.Regexp
	provider string
}

// inferRules map a bare model name to the provider that serves it. The first
// match on the lower-cased name wins.
var inferRules = []inferRule{
	{regexp.MustCompile(`^codex-auto-review$`), "codex"},
	{regexp.MustCompile(`^gpt-[56]\.`), "codex"},
	{regexp.MustCompile(`^gpt-6-`), "codex"},
	{regexp.MustCompile(`^gpt-daybreak-`), "codex"},
	{regexp.MustCompile(`^gpt-reserve`), "codex"},
	{regexp.MustCompile(`^claude-`), "anthropic"},
	{regexp.MustCompile(`^(us|eu|ap|global)\.(anthropic|meta|amazon|mistral|xai)\.`), "bedrock"},
	{regexp.MustCompile(`^gemini-`), "gemini"},
	{regexp.MustCompile(`^gpt-`), "openai"},
	{regexp.MustCompile(`^o[134]`), "openai"},
	{regexp.MustCompile(`^deepseek-`), "openrouter"},
}

func infer(model string) string {
	lower := strings.ToLower(model)
	for _, r := range inferRules {
		if r.re.MatchString(lower) {
			return r.provider
		}
	}
	return "openai"
}
