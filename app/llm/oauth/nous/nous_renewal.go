package nous

import (
	"context"
	"rllmx/app/llm/oauth"

	"rllmx/app/store/document"
)

// nousRenewal mints a new agent key, which is the key chat requests carry. The
// key's own expiry caps the account's, so the key is renewed before it dies.
func Renewal(ctx context.Context, c *oauth.TokenClient, f oauth.Flow, t oauth.Tokens, h map[string]string) (oauth.RefreshUpdate, error) {
	key, until, _, err := mintAgentKey(ctx, c, f, t.Access, h)
	if err != nil {
		return oauth.RefreshUpdate{}, err
	}
	return oauth.RefreshUpdate{Fields: map[string]string{"apiKey": document.Encode(key)}, ExpiresAt: until}, nil
}
