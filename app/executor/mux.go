package executor

import (
	"context"
	"errors"
	"time"

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
