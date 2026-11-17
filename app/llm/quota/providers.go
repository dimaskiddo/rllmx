package quota

import (
	"context"
	"strings"

	"rllmx/app/llm/upstream"
)

// authMask says which auth types a provider's reader serves. A login is served
// by every reader; a pasted key only by the providers whose plan it reports.
type authMask uint8

const (
	authOAuth authMask = 1 << iota
	authKey
)

func (m authMask) eligible(authType string) bool {
	switch strings.ToLower(authType) {
	case "oauth", "access_token":
		return m&authOAuth != 0
	case "apikey", "api_key":
		return m&authKey != 0
	}
	return false
}

// fetcher reads one provider's quota.
type fetcher struct {
	auth authMask
	run  func(ctx context.Context, r *Reader, e upstream.Entry, a Access) (Reading, error)
}

// fetchers is the dispatch table and the list of providers a connection's
// quota can be read for. A provider whose reading needs a handshake of its own
// is absent until that handshake is built.
var fetchers = map[string]fetcher{
	"github":            {auth: authOAuth, run: readGitHub},
	"gemini-cli":        {auth: authOAuth, run: readGeminiCLI},
	"antigravity":       {auth: authOAuth, run: readAntigravity},
	"claude":            {auth: authOAuth, run: readClaude},
	"codex":             {auth: authOAuth | authKey, run: readCodex},
	"kiro":              {auth: authOAuth | authKey, run: readKiro},
	"kiro-social":       {auth: authOAuth, run: readKiro},
	"glm":               {auth: authOAuth | authKey, run: readGLM},
	"glm-cn":            {auth: authOAuth | authKey, run: readGLM},
	"minimax":           {auth: authOAuth | authKey, run: readMiniMax},
	"minimax-cn":        {auth: authOAuth | authKey, run: readMiniMax},
	"kimi":              {auth: authOAuth | authKey, run: readKimi},
	"deepseek":          {auth: authKey, run: readDeepSeek},
	"opencode-go":       {auth: authKey, run: readOpenCode},
	"opencode-zen":      {auth: authKey, run: readOpenCode},
	"zed":               {auth: authOAuth, run: readZed},
	"cursor":            {auth: authOAuth, run: readCursor},
	"grok-cli":          {auth: authOAuth, run: readGrokCLI},
	"codebuddy-cn":      {auth: authOAuth | authKey, run: readCodeBuddy},
	"codebuddy-intl":    {auth: authOAuth | authKey, run: readCodeBuddy},
	"clinepass":         {auth: authOAuth | authKey, run: readCline},
	"cline":             {auth: authOAuth | authKey, run: readCline},
	"freebuff":          {auth: authOAuth, run: readFreebuff},
	"groq":              {auth: authKey, run: readGroq},
	"ollama":            {auth: authKey, run: readOllama},
	"vercel-ai-gateway": {auth: authKey, run: readVercel},
	"commandcode":       {auth: authKey, run: readCommandCode},
}

// Supports reports whether a connection of this provider has a quota reader.
func Supports(provider string) bool {
	_, ok := fetchers[provider]
	return ok
}

// Serves reports whether a reading can be asked for this provider and auth type.
// A dashboard lists exactly the connections a reading answers.
func Serves(provider, authType string) bool {
	f, ok := fetchers[provider]
	return ok && f.auth.eligible(authType)
}
