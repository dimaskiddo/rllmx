package catalog

import (
	"context"

	"rllmx/app/llm/routing"
	"rllmx/app/llm/upstream"
	"rllmx/app/store/repository"
)

// comboOwner is the owned_by of a combo entry, so a client can tell a named
// route from a model.
const comboOwner = "combo"

// Combos is what the catalog needs of the combo store: the stored combos and
// their members resolved the way a request resolves them.
type Combos interface {
	ComboList(ctx context.Context) ([]repository.Combo, error)
	Expand(ctx context.Context, name string) ([]routing.Member, error)
}

// comboEntries lists every chat combo as one callable model, in the store's
// order. A combo the members of which cannot be resolved is still listed, as
// the reference lists it, with nothing known about it.
func (c *Catalog) comboEntries(ctx context.Context) ([]Entry, error) {
	if c.Combos == nil {
		return nil, nil
	}
	list, err := c.Combos.ComboList(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(list))
	for _, cb := range list {
		if cb.Kind != "" && cb.Kind != kindLLM {
			continue
		}
		out = append(out, Entry{
			ID: cb.Name, Name: cb.Name, OwnedBy: comboOwner, Combo: cb.Name,
			Caps: c.comboCaps(ctx, cb.Name),
		})
	}
	return out, nil
}

// comboCaps aggregates the members' capabilities. Any member may answer, so a
// limit is the smallest one any of them knows. A modality is offered when any
// member has it, because the walk sends a turn to a member that can read it;
// tools are kept only when every member has them, since one that cannot call a
// tool may be the one that answers.
func (c *Catalog) comboCaps(ctx context.Context, name string) upstream.Capability {
	members, err := c.Combos.Expand(ctx, name)
	if err != nil {
		return upstream.Capability{}
	}
	caps := make([]upstream.Capability, 0, len(members))
	for _, m := range members {
		caps = append(caps, c.Caps.For(m.Provider, m.Model))
	}
	return aggregateCaps(caps)
}

func aggregateCaps(caps []upstream.Capability) upstream.Capability {
	if len(caps) == 0 {
		return upstream.Capability{}
	}
	// Reasoning is the first member's own answer; the rest are combined below.
	first := caps[0]
	out := upstream.Capability{
		Reasoning: first.Reasoning, ThinkingFormat: first.ThinkingFormat,
		ThinkingCanOff: first.ThinkingCanOff, ThinkingEffortSupported: first.ThinkingEffortSupported,
		ThinkingMin: first.ThinkingMin, ThinkingMax: first.ThinkingMax,
	}
	out.Tools = true
	for _, cp := range caps {
		out.Vision = out.Vision || cp.Vision
		out.PDF = out.PDF || cp.PDF
		out.AudioInput = out.AudioInput || cp.AudioInput
		out.VideoInput = out.VideoInput || cp.VideoInput
		out.ImageOutput = out.ImageOutput || cp.ImageOutput
		out.AudioOutput = out.AudioOutput || cp.AudioOutput
		out.Search = out.Search || cp.Search
		out.Tools = out.Tools && cp.Tools
		out.ContextWindow = smallest(out.ContextWindow, cp.ContextWindow)
		out.MaxOutput = smallest(out.MaxOutput, cp.MaxOutput)
	}
	return out
}

// smallest keeps the smaller positive limit; zero means unknown.
func smallest(a, b int) int {
	switch {
	case a <= 0:
		return b
	case b <= 0:
		return a
	case b < a:
		return b
	}
	return a
}
