package pipeline

import (
	"context"
	"time"

	"github.com/goravel/framework/contracts/log"

	"rllmx/app/account"
	"rllmx/app/analytics"
	"rllmx/app/conversation"
	"rllmx/app/document"
	"rllmx/app/executor"
	"rllmx/app/repository"
	"rllmx/app/routing"
	"rllmx/app/stream"
	"rllmx/app/translate"
	"rllmx/app/upstream"
)

// Resolver turns the model name a client sent into a provider and model.
type Resolver interface {
	Resolve(ctx context.Context, input string) (routing.Target, error)
}

// Selector picks the next usable account of a provider.
type Selector interface {
	Select(ctx context.Context, req account.Request) (account.Selection, error)
}

// Reporter records how an account did.
type Reporter interface {
	Fail(ctx context.Context, in account.FailInput) (account.Outcome, error)
	Succeed(ctx context.Context, connID, model string, started time.Time) error
}

// Nodes reads a user-defined provider node.
type Nodes interface {
	Get(ctx context.Context, id string) (repository.Node, bool, error)
}

// Prices prices a request. known is false for a model with no rates.
type Prices interface {
	Cost(provider, model string, u upstream.Usage) (cost float64, known bool)
}

// Usage takes the one row a request leaves behind.
type Usage interface {
	Enqueue(r analytics.Record) bool
}

// Overrides gives the operator's extra headers for a provider.
type Overrides interface {
	Headers(ctx context.Context, provider string) map[string]string
}

// Prefetch turns remote image URLs in a request into inline data.
type Prefetch interface {
	Inline(ctx context.Context, req *translate.Request) int
}

// Settings reads the stored settings document.
type Settings interface {
	Effective(ctx context.Context) (document.Doc, error)
}

// Deps is everything a Pipeline needs. Overrides, Settings, Prefetch, Log and Now are optional.
type Deps struct {
	Resolver  Resolver
	Selector  Selector
	Reporter  Reporter
	Executor  executor.Executor
	Nodes     Nodes
	Registry  *upstream.Registry
	Caps      *upstream.Capabilities
	Prices    Prices
	Usage     Usage
	Overrides Overrides
	Settings  Settings
	Prefetch  Prefetch
	// ResponseCache is the opt-in answer cache; nil is off.
	ResponseCache *ResponseCache
	// Refresher renews OAuth tokens; nil leaves them as stored.
	Refresher Refresher
	// Sessions names the conversation a request belongs to; nil makes one.
	Sessions *conversation.Store
	Timers   stream.Timers
	// BodyTimeout bounds reading a whole non-stream answer; zero means ten minutes.
	BodyTimeout time.Duration
	Log         log.Log
	Now         func() time.Time
}
