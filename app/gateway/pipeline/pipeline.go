// Package pipeline carries one client request through to one answered request
// and one usage row: parse, resolve, translate, walk the provider's accounts,
// answer. It has no HTTP types, so a controller turns a request into an Input
// and an Output into a response, and tests drive it with a fake upstream.
package pipeline

import (
	"context"
	"errors"
	"net/http"
	"time"

	"rllmx/app/access/identity"
	"rllmx/app/gateway/conversation"
	"rllmx/app/gateway/saver"
	"rllmx/app/gateway/skill"
	"rllmx/app/llm/routing"
	"rllmx/app/llm/stream"
	"rllmx/app/llm/translate"
	"rllmx/app/platform/failure"
	"rllmx/app/store/analytics"

	// Each of these registers its codec in init(); a binary that runs requests
	// needs them all, and the pipeline is what runs them.
	_ "rllmx/app/llm/translate/claude"
	_ "rllmx/app/llm/translate/commandcode"
	_ "rllmx/app/llm/translate/cursor"
	_ "rllmx/app/llm/translate/gemini"
	_ "rllmx/app/llm/translate/kiro"
	_ "rllmx/app/llm/translate/ollama"
	_ "rllmx/app/llm/translate/openai"
	_ "rllmx/app/llm/translate/responses"
)

var (
	errNoOpener = errors.New("pipeline: no way to open a stream")
	errNoCodec  = errors.New("pipeline: no codec for the client format")
)

// errIncomplete means Deps lacks a collaborator the pipeline cannot run without.
var errIncomplete = errors.New("pipeline: a required dependency is missing")

const defaultBodyTimeout = 10 * time.Minute

// Input is one client request, already bounded by the body limit.
type Input struct {
	Path      string
	Body      []byte
	Caller    identity.Caller
	RequestID string
	UserAgent string
	// Header holds the request headers the pipeline reads (see PickHeaders),
	// never the whole set.
	Header http.Header
	// AcceptJSON is set when the client asked for JSON and not for an event
	// stream. It turns off a default stream, never an explicit one.
	AcceptJSON bool
	// Cache is the client's X-Rllm-Cache wish: "use", "bypass" or empty.
	Cache string
	// Connection pins the request to one connection. Only an internal model
	// probe sets it: the pinned connection is tried once and no other account
	// answers in its place.
	Connection string
	// NoRules skips the skill rule hooks. The smart router sets it on its own
	// question, so a rule cannot rewrite or decorate that internal call.
	NoRules bool
	// Internal marks a call this router makes for itself, such as a smart
	// router's question or an auto plan request: no auto plan runs on it.
	Internal bool
}

// Output is the answer. A streamed answer has already been written through the
// Opener and carries only Streamed; anything else is written by the caller from
// Status, Header and Body.
type Output struct {
	Status   int
	Header   http.Header
	Body     []byte
	Streamed bool
}

// Opener starts the client's event stream. The pipeline calls it when the first
// frame is ready, not before, so an upstream that fails before sending anything
// is still answered with a plain error and the next account can be tried.
type Opener func() (stream.Client, error)

// Pipeline is safe for concurrent use.
type Pipeline struct{ d Deps }

// New checks the dependencies and returns a Pipeline.
func New(d Deps) (*Pipeline, error) {
	if d.Resolver == nil || d.Selector == nil || d.Reporter == nil || d.Executor == nil ||
		d.Registry == nil || d.Caps == nil || d.Usage == nil {
		return nil, errIncomplete
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.BodyTimeout <= 0 {
		d.BodyTimeout = defaultBodyTimeout
	}
	if d.MaxAnswer <= 0 {
		d.MaxAnswer = maxAnswer
	}
	if d.Sessions == nil {
		d.Sessions = conversation.NewStore(d.Now)
	}
	return &Pipeline{d: d}, nil
}

// Handle answers one request and enqueues its usage row exactly once, whatever
// the outcome. open is called only for a streamed answer.
func (p *Pipeline) Handle(ctx context.Context, in Input, open Opener) Output {
	c := &call{p: p, in: in, open: open, start: p.d.Now()}
	c.row = analytics.Record{
		Time: c.start, RequestID: in.RequestID, APIKeyID: in.Caller.ID, APIKeyName: in.Caller.Name,
		Endpoint: in.Path,
	}
	out := c.run(ctx)
	c.finish(out)
	return out
}

// Allows reports whether the caller may use the model string it names, without
// spending anything: it is how a route that names a model but calls no provider
// is checked. It fails closed: a name that does not resolve, or a store that
// cannot answer, is not allowed.
func (p *Pipeline) Allows(ctx context.Context, caller identity.Caller, model string) bool {
	if !caller.Restricted {
		return true
	}
	t, err := p.d.Resolver.Resolve(ctx, model)
	if err != nil {
		return false
	}
	allow, err := routing.NewAllow(ctx, p.d.Resolver, caller.Allow)
	if err != nil {
		return false
	}
	return allow.Target(t)
}

// call is the state of one request.
type call struct {
	p     *Pipeline
	in    Input
	open  Opener
	start time.Time
	row   analytics.Record

	format       string
	clientStream bool
	req          *translate.Request
	tctx         *translate.Ctx
	dest         destination
	body         []byte
	upStream     bool

	// cacheKey is set when the response cache was consulted for this request;
	// the answer is stored under it and carries the miss header.
	cacheKey  string
	cacheOpts cacheSettings

	requested string
	// asked is the model name the client sent, kept for the log: a combo or a
	// rule replaces requested as the request is routed.
	asked  string
	client string
	// cloakedNames maps the tools the last attempt renamed back to the client's
	// names; the passthrough stream restores them from it.
	cloakedNames map[string]string
	attempt      int
	attemptStart time.Time
	errClass     string
	saverStats   saver.Stats
	skillStats   skill.Stats

	// rowStatus replaces the response status in the row when they differ: a
	// stream that broke after its 200 was sent.
	rowStatus int
	// rules is what this request's declarative hooks matched.
	rules skill.Actions
	// planInjected marks that an auto plan was written into the request, which
	// the response cache must not answer for or store.
	planInjected bool
	// planTurn is the plan text to put back into a body re-read for a combo
	// member, because a member never reuses the previous one's request.
	planTurn string
	// The conversation's skill state, resolved once per call.
	sessionLoaded bool
	sessionKey    string
	sessionPrior  map[string]bool
	sessionNotice bool
	// sessionPending is what this call's attempts injected in full, recorded
	// once the call is answered.
	sessionPending []string
}

func (c *call) run(ctx context.Context) Output {
	if out, ok := c.parse(); !ok {
		return out
	}
	c.evaluateRules(ctx)
	if out, ok := c.bypass(ctx); ok {
		return out
	}
	if out, ok := c.autoPlan(ctx); ok {
		return out
	}
	if out, ok := c.resolve(ctx); !ok {
		return out
	}
	return c.serve(ctx)
}

func (c *call) failureFormat() failure.Format { return failure.ForPath(c.in.Path) }
