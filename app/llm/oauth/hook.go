package oauth

import (
	"context"
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/platform/text"
)

// Hook reads who a login belongs to from the tokens it just got, and may call
// the provider for more. h are the login's call headers for this account.
// Most hooks are best effort: what they cannot learn they leave out. A Hook that
// returns an error ends the login, and the error carries a class, never text.
type Hook func(ctx context.Context, c *TokenClient, f Flow, t Tokens, h map[string]string) (Account, error)

const (
	MaxProfileField = 256
	MaxCredential   = 4096
)

// CleanEmail returns an address that is short and free of control characters,
// else "".
func CleanEmail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > MaxProfileField || text.HasASCIIControl(s) || strings.ContainsAny(s, " \t") {
		return ""
	}
	return s
}

// CredentialText reports a value fit to be stored and sent as a credential.
func CredentialText(s string) bool {
	return s != "" && len(s) <= MaxCredential && !text.HasASCIIControl(s)
}

// ProfileText reads a short text field of a profile, or "" for anything else.
func ProfileText(raw []byte, key string) string {
	v := gjson.GetBytes(raw, key)
	s := strings.TrimSpace(v.String())
	if v.Type != gjson.String || len(s) > MaxProfileField || text.HasASCIIControl(s) {
		return ""
	}
	return s
}

func SetText(m map[string]string, k, v string) {
	if v != "" {
		m[k] = v
	}
}
