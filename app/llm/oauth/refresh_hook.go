package oauth

import (
	"context"
	"time"
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
