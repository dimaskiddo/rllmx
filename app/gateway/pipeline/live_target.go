package pipeline

import (
	"context"
	"errors"
	"net/http"

	"rllmx/app/gateway/account"
)

// ErrUnknownTarget means a provider's catalog does not know the model, so no
// wire format can be chosen for it.
var ErrUnknownTarget = errors.New("pipeline: the provider does not serve this model")

// liveFormat asks the provider which format a model is served in. The account
// is not yet chosen, so the selector picks one the usual way; the catalog is the
// same for every account of the provider.
func (c *call) liveFormat(ctx context.Context, d destination) (string, Output, bool) {
	sel, err := c.p.d.Selector.Select(ctx, account.Request{
		Provider: d.provider, Model: d.model, RequestedModel: c.requested, Hold: c.hold(),
	})
	if err != nil {
		return "", c.noAccount(ctx, err, nil, false), false
	}
	format, err := c.p.d.Targets.Format(ctx, sel.Conn, d.model)
	switch {
	case errors.Is(err, ErrUnknownTarget):
		return "", c.fail(http.StatusNotFound, "not_found", "Model not found"), false
	case err != nil:
		return "", c.internal(ctx, err), false
	}
	return format, Output{}, true
}
