package pipeline

import (
	"context"
	"errors"
	"net/http"

	"rllmx/app/executor"
	"rllmx/app/repository"
	"rllmx/app/routing"
	"rllmx/app/translate"
	"rllmx/app/upstream"
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
}

func (c *call) resolve(ctx context.Context) (Output, bool) {
	t, err := c.p.d.Resolver.Resolve(ctx, c.req.Model)
	switch {
	case errors.Is(err, routing.ErrEmpty), errors.Is(err, routing.ErrTooLong), errors.Is(err, routing.ErrBadInput):
		return c.fail(http.StatusBadRequest, "invalid_request", "Invalid model"), false
	case err != nil:
		return c.internal(ctx, err), false
	}
	c.row.Provider, c.row.Model = t.Provider, t.Model
	if t.Combo != "" {
		c.row.ComboName = t.Combo
		return c.fail(http.StatusNotFound, "not_found", "Model not found"), false
	}
	dest, out, ok := c.destinationOf(ctx, t)
	if !ok {
		return out, false
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
	if err != nil {
		return c.fail(http.StatusBadRequest, "invalid_request", "Invalid request body"), false
	}
	c.body = translate.Finish(body, c.p.d.Caps.ParamRules(d.provider, d.model))
	return Output{}, true
}

// needsInlineImages is true for the upstream formats that take no image URL.
func needsInlineImages(format string) bool {
	return format == translate.FormatGemini || format == translate.FormatOllama
}

func (c *call) url() string {
	d := c.dest
	if d.node != nil {
		base, _ := d.node.Doc().GetString("baseUrl")
		api, _ := d.node.Doc().GetString("apiType")
		return executor.NodeURL(d.node.Type, base, api)
	}
	return executor.RegistryURL(d.entry, d.format, d.model, c.upStream)
}
