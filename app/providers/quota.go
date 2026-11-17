package providers

import (
	"context"
	"errors"
	"fmt"

	"rllmx/app/llm/oauth"
	"rllmx/app/llm/quota"
	"rllmx/app/platform/network"
	"rllmx/app/store/repository"
)

// newQuotaReader builds the per-connection quota reader from the same pieces a
// request uses: the guarded client, the renewal the pipeline follows, and the
// credential rule of a request.
func newQuotaReader(h *HTTP) *quota.Reader {
	r := &quota.Reader{
		Reg: h.Registry,
		Client: network.NewClient(network.Options{
			ConnectTimeout: h.Upstream.ConnectTimeout, MaxRedirects: 0, Allow: h.Upstream.Allow,
			BodyCap: 1 << 20, Curves: h.Upstream.Curves,
		}),
		Zed: h.Zed,
		Renew: func(ctx context.Context, conn repository.Connection) (repository.Connection, error) {
			return h.Refresher.Renew(ctx, conn)
		},
		Access: func(ctx context.Context, conn repository.Connection) (quota.Access, error) {
			return quotaAccess(ctx, h, conn)
		},
	}
	return r
}

// quotaAccess is what a reading presents: the key of an API connection, or the
// login's token, renewed first when it is due.
func quotaAccess(ctx context.Context, h *HTTP, conn repository.Connection) (quota.Access, error) {
	oauthConn := conn.AuthType == "oauth" || conn.AuthType == "access_token"
	if !oauthConn {
		key, _ := conn.Doc().GetString("apiKey")
		return quota.Access{Token: key, Conn: conn}, nil
	}
	fresh, err := h.Refresher.Ready(ctx, conn)
	if errors.Is(err, oauth.ErrExpired) {
		return quota.Access{}, fmt.Errorf("%w: %v", quota.ErrExpired, err)
	}
	if err != nil {
		// A renewal that failed for another reason leaves the old token, which the
		// provider itself decides whether to refuse.
		fresh = conn
	}
	token, _ := fresh.Doc().GetString("accessToken")
	if token == "" {
		if key, _ := fresh.Doc().GetString("apiKey"); key != "" {
			token = key
		}
	}
	return quota.Access{Token: token, Conn: fresh}, nil
}
