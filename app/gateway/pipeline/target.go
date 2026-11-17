package pipeline

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"rllmx/app/llm/executor"
	"rllmx/app/llm/routing"
	"rllmx/app/llm/translate"
	"rllmx/app/llm/upstream"
	"rllmx/app/store/repository"
)

// destination is where a request goes: a registry provider or a user node.
type destination struct {
	provider string
	model    string
	// format is the wire format the upstream speaks.
	format string
	entry  upstream.Entry
	node   *repository.Node
	auth   *upstream.Auth
	// lane is the provider's endpoint for a client's own wire format, when its
	// data names one: the request goes there with no translation.
	lane *upstream.Lane
}

func (c *call) resolve(ctx context.Context) (Output, bool) {
	t, err := c.p.d.Resolver.Resolve(ctx, c.req.Model)
	switch {
	case errors.Is(err, routing.ErrEmpty), errors.Is(err, routing.ErrTooLong), errors.Is(err, routing.ErrBadInput):
		return c.fail(http.StatusBadRequest, "invalid_request", "Invalid model"), false
	case err != nil:
		return c.internal(ctx, err), false
	}
	// The key is checked on the resolved target, before the response cache and
	// before any account: a check after the cache would let a restricted key read
	// an answer cached for a model it may not call.
	if c.in.Caller.Restricted {
		allow, err := routing.NewAllow(ctx, c.p.d.Resolver, c.p.d.Registry, c.in.Caller.Allow)
		if err != nil {
			return c.internal(ctx, err), false
		}
		if !allow.Target(t) {
			return c.refuseModel(), false
		}
	}
	if out, ok := c.budget(ctx); !ok {
		return out, false
	}
	c.row.Provider, c.row.Model = t.Provider, t.Model
	// A media model is never a chat model: the two areas share one registry, so
	// the kind decides.
	if m, ok := c.p.d.Registry.Model(t.Provider, t.Model); ok && m.Kind != "" && m.Kind != upstream.KindLLM {
		return c.fail(http.StatusNotFound, "not_found", "Model not found"), false
	}
	if t.Combo != "" {
		if c.in.Connection != "" {
			// A pinned request names one connection, and a combo is not one.
			return c.fail(http.StatusBadRequest, "invalid_request", "A combo cannot be pinned to one connection"), false
		}
		// A combo name is routable: its members are tried in order, and this call
		// keeps the one row.
		return c.comboRun(ctx, t.Combo), false
	}
	dest, out, ok := c.destinationOf(ctx, t)
	if !ok {
		return out, false
	}
	// A provider whose wire format depends on the model asks about it before the
	// body is built; one that cannot answer is a local error, because a wrong
	// format is refused upstream with nothing a reader can act on.
	if dest.entry.Executor == upstream.ExecutorZed && c.p.d.Targets != nil {
		format, out, ok := c.liveFormat(ctx, dest)
		if !ok {
			return out, false
		}
		dest.format = format
	}
	c.dest = dest

	caps := c.p.d.Caps.For(dest.provider, dest.model)
	tc := c.tctx
	tc.TargetFormat, tc.Provider, tc.Model, tc.Caps, tc.Entry = dest.format, dest.provider, dest.model, caps, dest.entry
	tc.BudgetOf = c.p.d.Caps.Budget
	if store := newSignatureStore(c.p.d.Signatures, c.in.Caller.ID, dest.model); store != nil {
		tc.Calls = store
	}
	tc.Thinking = translate.ResolveThinking(t.Thinking, c.req.Thinking, translate.ModelThinking{}, caps)

	c.upStream = c.clientStream || dest.entry.ForceStream
	if out, hit := c.cached(ctx); hit {
		return out, false
	}
	return c.build(ctx, dest)
}

func (c *call) destinationOf(ctx context.Context, t routing.Target) (destination, Output, bool) {
	d := destination{provider: t.Provider, model: t.Model}
	if e, ok := c.p.d.Registry.ByID(t.Provider); ok {
		d.entry, d.format, d.auth = e, e.Transport.Format, e.Auth
		for _, m := range e.Models {
			if m.ID == t.Model && m.TargetFormat != "" {
				d.format = m.TargetFormat
			}
		}
		// A provider whose data names a Claude address sends every Claude-named
		// model there, listed or not: its live catalog gets new Claude models
		// before a data file does.
		if e.Executor == upstream.ExecutorGitHub && claudeNamed(t.Model) {
			d.format = upstream.FormatClaude
		}
		// A client whose own format has a lane is served there untranslated,
		// whatever format the model would otherwise be reached in.
		if l, ok := e.Transport.Lanes[c.format]; ok {
			d.format, d.lane = c.format, &l
			d.auth = laneAuth(l, d.format)
			// The provider's own static headers belong to its default address;
			// a lane carries its own.
			d.entry.Transport.Headers = l.Headers
		}
		if d.auth == nil {
			d.auth = defaultAuth(d.format)
		}
		return d, Output{}, true
	}
	kind := upstream.NodeKind(t.Provider)
	if kind == "" || kind == upstream.NodeTypeEmbedding || c.p.d.Nodes == nil {
		return d, c.fail(http.StatusNotFound, "not_found", "Model not found"), false
	}
	n, found, err := c.p.d.Nodes.Get(ctx, t.Provider)
	if err != nil {
		return d, c.internal(ctx, err), false
	}
	if !found {
		return d, c.fail(http.StatusNotFound, "not_found", "Model not found"), false
	}
	d.node = &n
	d.format = upstream.FormatOpenAI
	if kind == upstream.NodeTypeAnthropic {
		d.format = upstream.FormatClaude
	} else if api, _ := n.Doc().GetString("apiType"); api == "responses" {
		d.format = upstream.FormatResponses
	}
	d.auth = defaultAuth(d.format)
	return d, Output{}, true
}

// laneAuth is how a key is presented on a lane: the lane's own block, else the
// default for its format.
func laneAuth(l upstream.Lane, format string) *upstream.Auth {
	if l.Auth != nil {
		return l.Auth
	}
	return defaultAuth(format)
}

// claudeNamed reports a model whose name says it is a Claude model.
func claudeNamed(model string) bool {
	return strings.Contains(strings.ToLower(model), "claude")
}

// defaultAuth is how a key is presented when the provider's data names no block.
func defaultAuth(format string) *upstream.Auth {
	switch format {
	case upstream.FormatClaude:
		return &upstream.Auth{Scheme: upstream.SchemeRaw, Header: "x-api-key", Headers: map[string]string{"anthropic-version": "2023-06-01"}}
	case upstream.FormatGemini:
		return &upstream.Auth{Scheme: upstream.SchemeRaw, Header: "x-goog-api-key"}
	}
	return &upstream.Auth{Scheme: upstream.SchemeBearer, Header: "Authorization"}
}

// build translates the request once; every account gets the same body.
func (c *call) build(ctx context.Context, d destination) (Output, bool) {
	codec, ok := translate.Lookup(d.format)
	if !ok {
		return c.internal(ctx, errors.New("no codec for "+d.format)), false
	}
	c.req.Model, c.req.Stream = c.p.d.Registry.WireModel(d.provider, d.model), c.upStream
	translate.Prepare(c.req, c.tctx)
	if c.p.d.Prefetch != nil && (needsInlineImages(d.format) || d.entry.Transport.Quirks.InlineImages) {
		c.p.d.Prefetch.Inline(ctx, c.req)
	}
	body, err := codec.WriteRequest(c.req, c.tctx)
	switch {
	case errors.Is(err, translate.ErrImages):
		return c.fail(http.StatusBadRequest, "invalid_request", "This model does not accept images"), false
	case err != nil:
		return c.fail(http.StatusBadRequest, "invalid_request", "Invalid request body"), false
	}
	c.body = translate.Finish(body, c.p.d.Caps.ParamRules(d.provider, d.model))
	return Output{}, true
}

// needsInlineImages is true for the upstream formats that take no image URL.
func needsInlineImages(format string) bool {
	return translate.GeminiFamily(format) || format == translate.FormatOllama || format == translate.FormatKiro
}

func (c *call) url() string {
	d := c.dest
	if d.node != nil {
		base, _ := d.node.Doc().GetString("baseUrl")
		api, _ := d.node.Doc().GetString("apiType")
		return executor.NodeURL(d.node.Type, base, api)
	}
	if d.lane != nil {
		return executor.LaneURL(*d.lane)
	}
	return executor.RegistryURL(d.entry, d.format, d.model, c.upStream)
}
