package executor

import (
	"context"
	"net/http"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"rllmx/app/upstream"
	"rllmx/app/zed"
)

// The hosted service's wire values, which the request's provider field carries.
// They are not display names: an unrecognized value fails the request before the
// model is looked at.
const (
	zedProviderAnthropic = "anthropic"
	zedProviderOpenAI    = "open_ai"
	zedProviderGoogle    = "google"
	zedProviderXAI       = "x_ai"
	zedVersion           = "0.200.0"
)

// TokenExchanger turns an account's own token into the hosted one.
type TokenExchanger interface {
	Token(ctx context.Context, a zed.Account, force bool) (string, error)
}

// Zed sends to the hosted service, which wraps whichever provider's body the
// model belongs to. The account's token is exchanged for a hosted one first.
type Zed struct {
	Default *Default
	Tokens  TokenExchanger
}

// Execute sends one call. req.Key holds the account's own access token, and the
// account's data names who it belongs to and which organization it bills.
func (z *Zed) Execute(ctx context.Context, req Request) (Response, error) {
	if z.Tokens == nil {
		return Response{}, ErrBadAccount
	}
	acct := zed.Account{
		Access: req.Key, UserID: field(req, "userId"), SystemID: field(req, "systemId"),
		OrganizationID: field(req, "organizationId"),
	}
	if acct.Access == "" {
		return Response{}, ErrNoCredential
	}
	provider := zedProvider(req.Entry.Transport.Format)
	inner := zedInner(req, provider)
	if len(inner) == 0 {
		return Response{}, ErrBadAccount
	}
	payload, err := zedPayload(req, provider, inner)
	if err != nil {
		return Response{}, ErrBadAccount
	}
	req.Body = payload
	req.Auth = &upstream.Auth{Headers: map[string]string{
		"Accept":                                "application/x-ndjson, text/event-stream, */*",
		"User-Agent":                            "rllmx/zed",
		"x-zed-version":                         zedVersion,
		"x-zed-client-supports-status-messages": "true",
		"x-zed-client-supports-stream-ended-request-completion-status": "true",
	}}
	token, err := z.Tokens.Token(ctx, acct, false)
	if err != nil {
		return Response{}, ErrBadAccount
	}
	req.Auth.Headers["Authorization"] = "Bearer " + token
	req.Stream = true
	resp, err := z.Default.Execute(ctx, req)
	if err != nil || resp.Status != http.StatusUnauthorized {
		return resp, err
	}
	// A hosted token can end before its hour is up. One fresh token is tried
	// before the account is called unfit: the login's own token is not the one
	// that was refused.
	drain(resp.Body)
	fresh, terr := z.Tokens.Token(ctx, acct, true)
	if terr != nil {
		return resp, nil
	}
	req.Auth.Headers["Authorization"] = "Bearer " + fresh
	return z.Default.Execute(ctx, req)
}

// zedProvider maps the entry's format to the wire value the service expects.
func zedProvider(format string) string {
	switch format {
	case upstream.FormatClaude:
		return zedProviderAnthropic
	case upstream.FormatGemini:
		return zedProviderGoogle
	case upstream.FormatResponses:
		return zedProviderOpenAI
	}
	return zedProviderXAI
}

// zedInner fits the translated body to the provider's own shape. The envelope
// carries the provider's request as an object, and an OpenAI-shaped one is Chat
// Completions.
func zedInner(req Request, provider string) []byte {
	body := req.Body
	switch provider {
	case zedProviderGoogle:
		// The hosted backend speaks its own safety vocabulary, so the client's
		// settings are dropped and the service applies its defaults.
		return remove(body, "safetySettings")
	case zedProviderXAI:
		out, err := sjson.SetBytes(body, "model", req.Model)
		if err != nil {
			return body
		}
		out, err = sjson.SetBytes(out, "stream", req.Stream)
		if err != nil {
			return body
		}
		return out
	}
	return body
}

// zedPayload wraps the provider's body in the envelope the service reads.
func zedPayload(req Request, provider string, inner []byte) ([]byte, error) {
	body := []byte(`{"provider":` + quoteJSON(provider) + `,"model":` + quoteJSON(req.Model) + `}`)
	var err error
	if body, err = sjson.SetBytes(body, "thread_id", req.Session); err != nil {
		return nil, err
	}
	if pid := gjson.GetBytes(req.Body, "prompt_id").String(); pid != "" {
		if body, err = sjson.SetBytes(body, "prompt_id", pid); err != nil {
			return nil, err
		}
	}
	if body, err = sjson.SetRawBytes(body, "provider_request", inner); err != nil {
		return nil, err
	}
	return body, nil
}
