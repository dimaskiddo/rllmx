package executor

import (
	"context"
	"crypto/rsa"
	"errors"
	"net/http"
	"time"

	"rllmx/app/cache"
	"rllmx/app/upstream"
)

// ErrUnknownExecutor means a provider names an executor nobody registered. The
// call fails: falling back to the default would send a body shaped for another
// protocol.
var ErrUnknownExecutor = errors.New("executor: no such executor is registered")

// Mux sends a request to the executor its provider names. A request with no
// registry entry, a user node, goes to the default one.
type Mux struct {
	byName map[string]Executor
}

// Options are the settings the specialized executors need.
type Options struct {
	// PeekIdle bounds the silence tolerated while an answer's start is checked.
	PeekIdle time.Duration
	// VertexTokens trades a Vertex key for a bearer token; nil leaves the Vertex
	// executors able to serve raw API keys only.
	VertexTokens TokenSource
	// MimoCookies gets a Xiaomi MiMo desktop account's session cookie; nil keeps
	// those three ids on the cloud API.
	MimoCookies CookieSource
	// ZedTokens exchanges a Zed account's token for a hosted one; nil leaves the
	// Zed executor unable to send.
	ZedTokens TokenExchanger
	// FreebuffSessions claims the session Freebuff binds an account to; nil sends
	// every call without one.
	FreebuffSessions SessionSource
	// QoderKey signs a Qoder request; nil leaves those providers unable to send.
	QoderKey *rsa.PublicKey
	// Cache keeps what a provider's own catalog returned; nil fetches every call.
	Cache *cache.Cache
	// OpenCodeClient is the client the free lane goes through; its TLS offer is
	// part of that lane's gate. Nil takes the shared client.
	OpenCodeClient *http.Client
}

// NewMux builds a Mux around the default executor, with the specialized ones
// beside it. The names must be the ones a data file may use.
func NewMux(d *Default, o Options) *Mux {
	return &Mux{byName: map[string]Executor{
		upstream.ExecutorDefault: d,
		upstream.ExecutorAzure:   &Azure{Default: d},
		upstream.ExecutorCodex:   &Codex{Default: d, PeekIdle: o.PeekIdle},
		upstream.ExecutorGrokCLI: &Grok{Default: d},
		upstream.ExecutorGitHub:  &GitHub{Default: d},

		upstream.ExecutorCodeBuddyCN:   &CodeBuddy{Default: d},
		upstream.ExecutorCodeBuddyIntl: &CodeBuddy{Default: d, Intl: true},
		upstream.ExecutorMinimaxCode:   &MinimaxCode{Default: d},

		upstream.ExecutorGeminiCLI:     &CloudCode{Default: d},
		upstream.ExecutorAntigravity:   &CloudCode{Default: d, Antigravity: true},
		upstream.ExecutorVertex:        &Vertex{Default: d, Tokens: o.VertexTokens},
		upstream.ExecutorVertexPartner: &Vertex{Default: d, Tokens: o.VertexTokens, Partner: true},

		upstream.ExecutorBedrock:    &Bedrock{Default: d},
		upstream.ExecutorBedrockXAI: &Bedrock{Default: d, XAI: true},
		upstream.ExecutorKiro:       &Kiro{Default: d},

		upstream.ExecutorOllamaLocal:     &OllamaLocal{Default: d},
		upstream.ExecutorXiaomiTokenPlan: &XiaomiTokenPlan{Default: d},
		upstream.ExecutorXiaomiMimo:      &XiaomiMimo{Default: d, Cookies: o.MimoCookies},
		upstream.ExecutorCommandCode:     &CommandCode{Default: d, PeekIdle: o.PeekIdle},
		upstream.ExecutorOpenCode:        &OpenCodeFree{Default: d, Client: o.OpenCodeClient},
		upstream.ExecutorOpenCodeGo:      &OpenCodeKeyed{Default: d},
		upstream.ExecutorOpenCodeZen:     &OpenCodeKeyed{Default: d, Zen: true},
		upstream.ExecutorZed:             &Zed{Default: d, Tokens: o.ZedTokens},
		upstream.ExecutorFreebuff:        &Freebuff{Default: d, Sessions: o.FreebuffSessions},
		upstream.ExecutorQoder:           &Qoder{Default: d, PeekIdle: o.PeekIdle, Cache: o.Cache, Public: o.QoderKey},
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
func (m *Mux) Execute(ctx context.Context, req Request) (Response, error) {
	name := req.Entry.Executor
	if name == "" {
		name = upstream.ExecutorDefault
	}
	e, ok := m.byName[name]
	if !ok {
		return Response{}, ErrUnknownExecutor
	}
	return e.Execute(ctx, req)
}
