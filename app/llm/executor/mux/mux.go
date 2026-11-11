package mux

import (
	"context"
	"crypto/rsa"
	"errors"
	"net/http"
	"time"

	"rllmx/app/llm/executor"
	"rllmx/app/llm/executor/aws"
	"rllmx/app/llm/executor/codebuddy"
	"rllmx/app/llm/executor/commandcode"
	"rllmx/app/llm/executor/cursor"
	"rllmx/app/llm/executor/deepseek"
	"rllmx/app/llm/executor/freebuff"
	"rllmx/app/llm/executor/google"
	"rllmx/app/llm/executor/openai"
	"rllmx/app/llm/executor/opencode"
	"rllmx/app/llm/executor/qoder"
	"rllmx/app/llm/executor/xiaomi"
	"rllmx/app/llm/executor/zed"
	"rllmx/app/llm/upstream"
	"rllmx/app/platform/cache"
)

// ErrUnknownExecutor means a provider names an executor nobody registered. The
// call fails: falling back to the default would send a body shaped for another
// protocol.
var ErrUnknownExecutor = errors.New("executor: no such executor is registered")

// Mux sends a request to the executor its provider names. A request with no
// registry entry, a user node, goes to the default one.
type Mux struct {
	byName map[string]executor.Executor
}

// Options are the settings the specialized executors need.
type Options struct {
	// PeekIdle bounds the silence tolerated while an answer's start is checked.
	PeekIdle time.Duration
	// VertexTokens trades a Vertex key for a bearer token; nil leaves the Vertex
	// executors able to serve raw API keys only.
	VertexTokens google.TokenSource
	// MimoCookies gets a Xiaomi MiMo desktop account's session cookie; nil keeps
	// those three ids on the cloud API.
	MimoCookies executor.CookieSource
	// ZedTokens exchanges a Zed account's token for a hosted one; nil leaves the
	// Zed executor unable to send.
	ZedTokens zed.TokenExchanger
	// FreebuffSessions claims the session Freebuff binds an account to; nil sends
	// every call without one.
	FreebuffSessions freebuff.SessionSource
	// QoderKey signs a Qoder request; nil leaves those providers unable to send.
	QoderKey *rsa.PublicKey
	// Cache keeps what a provider's own catalog returned; nil fetches every call.
	Cache *cache.Cache
	// OpenCodeClient is the client the free lane goes through; its TLS offer is
	// part of that lane's gate. Nil takes the shared client.
	OpenCodeClient *http.Client
}

// New builds a Mux around the default executor, with the specialized ones
// beside it. The names must be the ones a data file may use.
func New(d *executor.Default, o Options) *Mux {
	return &Mux{byName: map[string]executor.Executor{
		upstream.ExecutorDefault: d,
		upstream.ExecutorAzure:   &openai.Azure{Default: d},
		upstream.ExecutorCodex:   &openai.Codex{Default: d, PeekIdle: o.PeekIdle},
		upstream.ExecutorGrokCLI: &openai.Grok{Default: d},
		upstream.ExecutorGitHub:  &openai.GitHub{Default: d},

		upstream.ExecutorCodeBuddyCN:   &codebuddy.CodeBuddy{Default: d},
		upstream.ExecutorCodeBuddyIntl: &codebuddy.CodeBuddy{Default: d, Intl: true},
		upstream.ExecutorMinimaxCode:   &codebuddy.MinimaxCode{Default: d},

		upstream.ExecutorGeminiCLI:     &google.CloudCode{Default: d},
		upstream.ExecutorAntigravity:   &google.CloudCode{Default: d, Antigravity: true},
		upstream.ExecutorVertex:        &google.Vertex{Default: d, Tokens: o.VertexTokens},
		upstream.ExecutorVertexPartner: &google.Vertex{Default: d, Tokens: o.VertexTokens, Partner: true},

		upstream.ExecutorBedrock:    &aws.Bedrock{Default: d},
		upstream.ExecutorBedrockXAI: &aws.Bedrock{Default: d, XAI: true},
		upstream.ExecutorKiro:       &aws.Kiro{Default: d},

		upstream.ExecutorOllamaLocal:     &openai.OllamaLocal{Default: d},
		upstream.ExecutorXiaomiTokenPlan: &xiaomi.XiaomiTokenPlan{Default: d},
		upstream.ExecutorXiaomiMimo:      &xiaomi.XiaomiMimo{Default: d, Cookies: o.MimoCookies},
		upstream.ExecutorCommandCode:     &commandcode.CommandCode{Default: d, PeekIdle: o.PeekIdle},
		upstream.ExecutorOpenCode:        &opencode.OpenCodeFree{Default: d, Client: o.OpenCodeClient},
		upstream.ExecutorOpenCodeGo:      &opencode.OpenCodeKeyed{Default: d},
		upstream.ExecutorOpenCodeZen:     &opencode.OpenCodeKeyed{Default: d, Zen: true},
		upstream.ExecutorZed:             &zed.Executor{Default: d, Tokens: o.ZedTokens},
		upstream.ExecutorFreebuff:        &freebuff.Freebuff{Default: d, Sessions: o.FreebuffSessions},
		upstream.ExecutorQoder:           &qoder.Qoder{Default: d, PeekIdle: o.PeekIdle, Cache: o.Cache, Public: o.QoderKey},
		upstream.ExecutorCursor:          &cursor.Cursor{Default: d, PeekIdle: o.PeekIdle, Cache: o.Cache},
		upstream.ExecutorKimchi:          &openai.Kimchi{Default: d},
		upstream.ExecutorDeepseekWeb:     &deepseek.Executor{Default: d, Cache: o.Cache},
	}}
}

// Names lists the registered executors.
func (m *Mux) Names() []string {
	out := make([]string, 0, len(m.byName))
	for n := range m.byName {
		out = append(out, n)
	}
	return out
}

// Execute sends req through the executor named by its entry.
func (m *Mux) Execute(ctx context.Context, req executor.Request) (executor.Response, error) {
	name := req.Entry.Executor
	if name == "" {
		name = upstream.ExecutorDefault
	}
	e, ok := m.byName[name]
	if !ok {
		return executor.Response{}, ErrUnknownExecutor
	}
	return e.Execute(ctx, req)
}
