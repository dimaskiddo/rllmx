package oauth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
)

// A code dialect is a code login whose exchange and renewal are not the plain
// token calls: the code may carry the tokens itself, and the renewal has its
// own body and envelope. The rest of the login (the session, the state, the
// hook, the storage) is the same engine.
type CodeDialect struct {
	Exchange func(ctx context.Context, c *TokenClient, f Flow, in CodeExchange) (Tokens, error)
	Refresh  func(ctx context.Context, c *TokenClient, f Flow, refreshToken string, h map[string]string) (Tokens, error)
	// Authorize builds the address of a sign-in that makes its own key, and
	// returns the verifier the engine keeps for the exchange. nil uses URL.
	Authorize func(f Flow) (authURL, verifier string, err error)
	// CustomURL builds the address of a provider that owns its redirect scheme,
	// so its parameters are not the standard ones.
	CustomURL func(f Flow, state, challenge string) string
	// Variant picks which of a provider's several logins this is.
	Variant func(f Flow, variant string) (Flow, error)
	// Variants are the values Variant accepts, for a dashboard that has to
	// offer the choice.
	Variants []string
	// Paste finishes a login whose provider answers a scheme no server receives.
	Paste bool
	// RenewParams maps a flow parameter a renewal needs to the account field
	// that recorded it.
	RenewParams map[string]string
}

// CodeExchange is what a code login's exchange works from: the code the browser
// brought back, the verifier the login made, and the redirect it sent.
type CodeExchange struct {
	Code     string
	Verifier string
	Redirect string
}

// NewNonce is 16 random bytes in hex, for a provider that wants a nonce.
func NewNonce() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
