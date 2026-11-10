package executor

import (
	"context"
	"net/url"
	"strings"

	"rllmx/app/upstream"
)

// ollamaLocalDefault is where a local daemon listens when the account names no
// address of its own.
const ollamaLocalDefault = "http://localhost:11434"

// OllamaLocal serves a daemon on this machine or the operator's network. The
// address is the account's own, so the provider's data cannot be the one that
// decides where a request goes; a local address still passes the dialer's
// private-address rule.
type OllamaLocal struct {
	Default *Default
}

// Execute sends the Ollama body to the account's daemon.
func (o *OllamaLocal) Execute(ctx context.Context, req Request) (Response, error) {
	target, err := ollamaAddress(req)
	if err != nil {
		return Response{}, err
	}
	req.URL = target
	// A daemon on loopback usually runs without a key. Sending an empty bearer
	// earns a refusal nobody can read, so the credential is dropped entirely.
	if req.Key == "" {
		req.Auth, req.Entry.Auth = nil, nil
	} else {
		req.Auth = &upstream.Auth{Scheme: upstream.SchemeBearer, Header: "Authorization"}
	}
	return o.Default.Execute(ctx, req)
}

// ollamaAddress joins the account's own server address with the chat path. An
// account that names none gets the default host, so a stock install connects
// with no configuration at all.
func ollamaAddress(req Request) (string, error) {
	base := ollamaLocalDefault
	if req.Account != nil {
		if raw := strings.TrimSpace(req.Account("baseUrl")); raw != "" {
			base = raw
		}
	}
	base = strings.TrimSuffix(base, "/")
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(u.Scheme != "http" && u.Scheme != "https") {
		return "", ErrBadAccount
	}
	return base + "/api/chat", nil
}
