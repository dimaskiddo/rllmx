package oauth

import (
	"context"
)

// A dialect is a device login that does not follow RFC 8628. It replaces the
// start and the poll; the rest of the login (the session, the interval, the
// hook, the storage) is the same engine.
type DeviceDialect struct {
	Start func(ctx context.Context, c *TokenClient, f Flow, h map[string]string) (DeviceGrant, error)
	Poll  func(ctx context.Context, c *TokenClient, f Flow, s DeviceState, h map[string]string) (Tokens, PollState, error)
	// Refresh replaces the standard renewal when the provider has its own.
	Refresh func(ctx context.Context, c *TokenClient, f Flow, refreshToken string, h map[string]string) (Tokens, error)
}

// DeviceState is what a poll needs from the server-side login: the device code
// and what the dialect's start kept for it. None of it reaches the browser.
type DeviceState struct {
	Code     string
	Verifier string
	Extra    string
}
