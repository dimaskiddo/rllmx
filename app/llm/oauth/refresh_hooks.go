package oauth

import (
	"context"
	"time"

	"rllmx/app/store/document"
)

// RefreshUpdate is what a renewal step adds to the fields a renewal stores.
type RefreshUpdate struct {
	Fields    map[string]string
	ExpiresAt time.Time
}

// RefreshHook runs after a renewal succeeded. It may call the provider again,
// for a credential that is minted from the new token. An error keeps what the
// account already holds.
type RefreshHook func(ctx context.Context, c *TokenClient, f Flow, t Tokens, h map[string]string) (RefreshUpdate, error)

// nousRenewal mints a new agent key, which is the key chat requests carry. The
// key's own expiry caps the account's, so the key is renewed before it dies.
func nousRenewal(ctx context.Context, c *TokenClient, f Flow, t Tokens, h map[string]string) (RefreshUpdate, error) {
	key, until, _ := mintAgentKey(ctx, c, f, t.Access, h)
	if key == "" {
		return RefreshUpdate{}, &Error{ClassRejected}
	}
	return RefreshUpdate{Fields: map[string]string{"apiKey": document.Encode(key)}, ExpiresAt: until}, nil
}
