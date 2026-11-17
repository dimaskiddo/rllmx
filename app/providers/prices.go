package providers

import (
	"context"

	"github.com/goravel/framework/contracts/foundation"

	"rllmx/app/llm/upstream"
	"rllmx/app/store/repository"
)

// PriceSource builds the price table once. The pipeline prices a request with
// it and the suggest route ranks on it, so both read the same numbers.
type PriceSource struct{ lazy[upstream.Table] }

// NewPriceSource returns a source over the embedded rates and the operator's
// overrides.
func NewPriceSource(app foundation.Application) *PriceSource {
	s := &PriceSource{}
	s.build = func(ctx context.Context) (*upstream.Table, error) { return buildPrices(app, ctx) }
	return s
}

// Get returns the table, building it if no earlier call did.
func (s *PriceSource) Get() (*upstream.Table, error) { return s.get() }

// Reset drops the built table. A pricing write calls it, then Get, so the next
// request prices with the new cards.
func (s *PriceSource) Reset() { s.reset() }

// priceReader prices each request from the source, so a reset reaches the
// pipeline instead of a table captured when the pipeline was built.
type priceReader struct{ src *PriceSource }

func (p priceReader) Cost(provider, model string, u upstream.Usage) (float64, bool) {
	t, err := p.src.Get()
	if err != nil {
		return 0, false
	}
	return t.Cost(provider, model, u)
}

func buildPrices(app foundation.Application, ctx context.Context) (*upstream.Table, error) {
	h := HTTPOf(app)
	store := DataOf(app).Runner.Store()
	if store == nil {
		return nil, ErrStoreNotOpen
	}
	table, err := upstream.DefaultTable(h.Registry)
	if err != nil {
		return nil, err
	}
	raw, err := store.KV().List(ctx, repository.ScopePricing)
	if err != nil {
		app.MakeLog().Warningf("pipeline: pricing overrides not read: %v", err)
		return table, nil
	}
	return table.WithOverrides(upstream.ParseOverrides(raw, func(format string, args ...any) {
		app.MakeLog().Warningf(format, args...)
	})), nil
}
