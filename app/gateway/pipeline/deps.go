package pipeline

import (
	"context"
	"time"

	"github.com/goravel/framework/contracts/log"

	"rllmx/app/gateway/account"
	"rllmx/app/gateway/combo"
	"rllmx/app/gateway/conversation"
	"rllmx/app/llm/catalog"
	"rllmx/app/llm/executor"
	"rllmx/app/llm/routing"
	"rllmx/app/llm/stream"
	"rllmx/app/llm/translate"
	"rllmx/app/llm/upstream"
	"rllmx/app/store/analytics"
	"rllmx/app/store/document"
	"rllmx/app/store/repository"
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

// Projects finds an account's Cloud Code project with its access token.
type Projects interface {
	Project(ctx context.Context, conn repository.Connection, access string) (string, error)
}

// Quota is the Antigravity quota gate. Skip reads memory only; Refused is asked
// after a quota refusal and may fetch the account's live limits.
type Quota interface {
	Skip(account, model string) (time.Time, bool)
	Refused(ctx context.Context, account, access, model string) (time.Time, bool)
	Succeeded(account, model string)
}

// Health tracks how each provider is doing. Record takes one finished attempt;
// Open says the provider's breaker is currently turned away.
type Health interface {
	Record(provider string, ok bool, latency time.Duration)
	Open(provider string) bool
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

// Inflight counts the requests running now. The release is idempotent and says
// whether the attempt failed.
type Inflight interface {
	Start(provider, model, connID string) (release func(failed bool))
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

// Targets learns a model's wire format from the provider, for a provider whose
// catalog decides it. A format it cannot name ends the request locally.
type Targets interface {
	// Format returns the format a model is reached in, or ErrUnknownTarget.
	Format(ctx context.Context, conn repository.Connection, model string) (string, error)
}

// Catalog tells whether the chosen account's provider lists a model, so a
// request for an id no account serves is refused before anything is sent.
type Catalog interface {
	Known(ctx context.Context, provider string, conn repository.Connection, model string) catalog.Verdict
}

// Deps is everything a Pipeline needs. Overrides, Settings, Prefetch, Log and Now are optional.
type Deps struct {
	Resolver Resolver
	Selector Selector
	Reporter Reporter
	Executor executor.Executor
	Nodes    Nodes
	Registry *upstream.Registry
	Caps     *upstream.Capabilities
	Prices   Prices
	Usage    Usage
	Budget   Budget
	// Rotation is the per-combo round-robin position; nil starts every combo at
	// its first member.
	Rotation *combo.Rotation
	// Sticky remembers a smart combo's last choice per session; nil keeps only
	// the declared order.
	Sticky    *combo.Sticky
	Overrides Overrides
	Settings  Settings
	Prefetch  Prefetch
	// ResponseCache is the opt-in answer cache; nil is off.
	ResponseCache *ResponseCache
	// Signatures keeps provider thought signatures between turns; nil means a
	// signature the client drops is not restored.
	Signatures Loader
	// Replay keeps a conversation's opening turn for an upstream that caches a
	// prompt from it; nil rebuilds that turn on every request.
	Replay Loader
	// Refresher renews OAuth tokens; nil leaves them as stored.
	Refresher Refresher
	// Minter makes the credential of a provider whose login is traded for one;
	// nil sends the stored token.
	Minter Minter
	// Projects finds the Cloud Code project of a Google account that has none
	// stored; nil leaves such an account to fail.
	Projects Projects
	// Quota passes over an Antigravity account whose model is spent; nil leaves
	// that to the common lock rules.
	Quota Quota
	// Health feeds the per-provider breaker and orders combo members; nil
	// neither records nor skips.
	Health Health
	// Inflight feeds the dashboard's live request list; nil tracks nothing.
	Inflight Inflight
	// Targets names the wire format of a provider whose catalog decides it.
	Targets Targets
	// Catalog reads the provider's own model list before a send; nil leaves
	// every model to the provider.
	Catalog Catalog
	// Sessions names the conversation a request belongs to; nil makes one.
	Sessions *conversation.Store
	Timers   stream.Timers
	// BodyTimeout bounds reading a whole non-stream answer; zero means ten minutes.
	BodyTimeout time.Duration
	// MaxAnswer bounds a buffered upstream answer in bytes; zero means 64 MiB.
	MaxAnswer int64
	Log       log.Log
	Now       func() time.Time
	// ClaudeCLI is the claude-cli/x.y.z fingerprint; nil names the build's floor.
	ClaudeCLI *upstream.ClaudeCLI
}
