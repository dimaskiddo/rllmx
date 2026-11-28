package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"time"

	"rllmx/app/llm/upstream"
	"rllmx/app/platform/text"
)

// ErrUnreachable means no attempt got an answer: the dial failed, the TLS check
// failed or the headers did not arrive. It never carries the transport's text,
// which can hold the address.
var ErrUnreachable = errors.New("executor: the upstream could not be reached")

// Default is the executor every provider uses until it names its own. It takes
// everything from the request: address, format, auth block, headers.
type Default struct {
	Client *http.Client
	// Retry replaces defaultRetry when set.
	Retry map[int]Rule
	// Sleep returns a channel that fires after d; nil uses a real timer. Tests
	// pass a fake so retries cost no time.
	Sleep func(time.Duration) <-chan time.Time
}

// bounded returns d, or a copy whose client carries the provider's own wait
// bound: the client's timeout for the whole exchange, and its transport's header
// timeout, because a client timeout alone lets a written body wait on headers
// forever. The shared client is never mutated.
func (d *Default) bounded(timeout time.Duration) *Default {
	if timeout <= 0 || d.Client == nil {
		return d
	}
	strict := *d.Client
	strict.Timeout = timeout
	switch t := strict.Transport.(type) {
	case *http.Transport:
		copied := t.Clone()
		copied.ResponseHeaderTimeout = timeout
		strict.Transport = copied
	case interface {
		WithResponseHeaderTimeout(time.Duration) http.RoundTripper
	}:
		strict.Transport = t.WithResponseHeaderTimeout(timeout)
	}
	return &Default{Client: &strict, Retry: d.Retry, Sleep: d.Sleep}
}

// Execute sends req. A status in the retry table is tried again in place, with
// the earlier body drained first; the last answer is returned as it is, so the
// caller sees the real status. A network failure follows the 502 rule.
func (d *Default) Execute(ctx context.Context, req Request) (Response, error) {
	if req.Key == "" && authRequired(req) {
		return Response{}, ErrNoCredential
	}
	table := d.RetryRules(req.Entry)
	target, err := fillAccount(req)
	if err != nil {
		return Response{}, err
	}
	req.URL = target
	d = d.bounded(req.Timeout)
	tries := map[int]int{}
	replayed := false
	// addresses are the provider's other addresses, tried in data order when
	// this one refuses with a 429 that is not retried in place. An executor that
	// orders its own hosts (kiro) names an executor, so it is left alone.
	addresses := []string{req.URL}
	if req.Entry.Executor == "" || req.Entry.Executor == upstream.ExecutorDefault {
		addresses = fallbackURLs(req)
	}
	address := 0
	for {
		resp, err := d.Send(ctx, req)
		// A pooled socket the far end dropped silently costs one immediate
		// replay on a fresh connection, before any wait: the request was never
		// written, so sending it again sends it once.
		if err != nil && !replayed && ctx.Err() == nil && errors.Is(err, errStaleSocket) {
			replayed = true
			d.Client.CloseIdleConnections()
			resp, err = d.Send(ctx, req)
		}
		status := resp.Status
		if err != nil {
			if ctx.Err() != nil {
				return Response{}, ctx.Err()
			}
			status = http.StatusBadGateway
		}
		rule, retryable := table[status]
		if !retryable || tries[status] >= rule.Attempts {
			// A second address is worth one try, and only for the status that
			// means this one is busy: another address of the same provider is a
			// different edge, not a different account.
			if status == http.StatusTooManyRequests && err == nil && address+1 < len(addresses) {
				address++
				req.URL = addresses[address]
				tries = map[int]int{}
				Drain(resp.Body)
				continue
			}
			if err != nil {
				return Response{}, ErrUnreachable
			}
			return resp, nil
		}
		tries[status]++
		if err == nil {
			Drain(resp.Body)
		}
		if status == http.StatusBadGateway {
			// A 502 may be an edge that killed the connection it handed us; the
			// next attempt must not reuse a pooled one.
			d.Client.CloseIdleConnections()
		}
		if werr := Wait(ctx, rule.Backoff(), d.Sleep); werr != nil {
			return Response{}, werr
		}
	}
}

// fallbackURLs is the provider's addresses in the order they are tried: its own
// first, then the data file's others. A provider with one address returns one.
func fallbackURLs(req Request) []string {
	out := []string{req.URL}
	base := req.URL
	if i := strings.Index(base, "?"); i >= 0 {
		base = base[:i]
	}
	for _, other := range req.Entry.Transport.BaseURLs {
		if other != "" && other != base && !strings.Contains(other, "{") {
			out = append(out, other)
		}
	}
	return out
}

// RetryRules is the retry table in force for a provider: the executor's own, or
// the default one, with the provider's overrides laid over it.
func (d *Default) RetryRules(e upstream.Entry) map[int]Rule {
	table := d.Retry
	if table == nil {
		table = defaultRetry
	}
	return retryTable(table, e)
}

// authRequired reports whether the key is the credential. A block with no scheme
// sets no credential from it: the request carries its own headers instead, as an
// account-session cookie does.
func authRequired(req Request) bool {
	auth := req.Auth
	if auth == nil {
		auth = req.Entry.Auth
	}
	return auth != nil && auth.Scheme != ""
}

func (d *Default) Send(ctx context.Context, req Request) (Response, error) {
	auth := req.Auth
	if auth == nil {
		auth = req.Entry.Auth
	}
	target := req.URL
	if auth != nil {
		target = auth.WithKey(target, req.Key)
	}
	method := req.Method
	if method == "" {
		method = http.MethodPost
	}
	hr, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(req.Body))
	if err != nil {
		return Response{}, fmt.Errorf("executor: build request: %w", ErrUnreachable)
	}
	Headers(hr.Header, req, auth)
	if req.Sign != nil {
		if err := req.Sign(method, target, hr.Header, req.Body); err != nil {
			return Response{}, fmt.Errorf("%w: %w", ErrBadAccount, err)
		}
	}
	client := d.Client
	if req.NoRedirect {
		// A copy, so one provider's policy never becomes every provider's: the
		// shared client is reused across requests and its transport is not copied.
		strict := *d.Client
		strict.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		client = &strict
	}
	resp, err := client.Do(hr)
	if err != nil {
		if isStaleSocketError(err) {
			return Response{}, fmt.Errorf("%w: %w", ErrUnreachable, errStaleSocket)
		}
		return Response{}, ErrUnreachable
	}
	return Response{Status: resp.StatusCode, Header: resp.Header, Body: resp.Body}, nil
}

// Headers fills h in the order that lets nothing a data file or an operator
// writes replace the credential: content type, the provider's transport
// Headers, its auth Headers and the key, the stream marker, and last the
// operator's overrides, from which credential and framing Headers are dropped
// again here. A provider that named its own Accept keeps it: Bedrock frames a
// stream as a binary event stream, not as SSE.
func Headers(h http.Header, req Request, auth *upstream.Auth) {
	h.Set("Content-Type", "application/json")
	for k, v := range req.Entry.Transport.Headers {
		h.Set(k, v)
	}
	if auth != nil {
		for k, v := range auth.RequestHeaders(req.Key) {
			h.Set(k, v)
		}
	}
	if req.Stream && h.Get("Accept") == "" {
		h.Set("Accept", "text/event-stream")
	}
	for k, v := range req.Overrides {
		if upstream.ReservedHeader(k) || !upstream.HeaderName(k) || text.HasASCIIControl(v) {
			continue
		}
		if auth != nil && strings.EqualFold(k, auth.Header) {
			continue
		}
		h.Set(k, v)
	}
}

// WithHeaders copies h and sets each given header.
func WithHeaders(h, add map[string]string) map[string]string {
	out := make(map[string]string, len(h)+len(add))
	maps.Copy(out, h)
	maps.Copy(out, add)
	return out
}
