package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"rllmx/app/text"
	"rllmx/app/upstream"
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

// Execute sends req. A status in the retry table is tried again in place, with
// the earlier body drained first; the last answer is returned as it is, so the
// caller sees the real status. A network failure follows the 502 rule.
func (d *Default) Execute(ctx context.Context, req Request) (Response, error) {
	if req.Key == "" && authRequired(req) {
		return Response{}, ErrNoCredential
	}
	table := d.Retry
	if table == nil {
		table = defaultRetry
	}
	table = retryTable(table, req.Entry)
	target, err := fillAccount(req)
	if err != nil {
		return Response{}, err
	}
	req.URL = target
	tries := map[int]int{}
	for {
		resp, err := d.once(ctx, req)
		status := resp.Status
		if err != nil {
			if ctx.Err() != nil {
				return Response{}, ctx.Err()
			}
			status = http.StatusBadGateway
		}
		rule, retryable := table[status]
		if !retryable || tries[status] >= rule.Attempts {
			if err != nil {
				return Response{}, ErrUnreachable
			}
			return resp, nil
		}
		tries[status]++
		if err == nil {
			drain(resp.Body)
		}
		if werr := wait(ctx, rule.delay(), d.Sleep); werr != nil {
			return Response{}, werr
		}
	}
}

func authRequired(req Request) bool {
	return req.Auth != nil || req.Entry.Auth != nil
}

func (d *Default) once(ctx context.Context, req Request) (Response, error) {
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
	headers(hr.Header, req, auth)
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
		return Response{}, ErrUnreachable
	}
	return Response{Status: resp.StatusCode, Header: resp.Header, Body: resp.Body}, nil
}

// headers fills h in the order that lets nothing a data file or an operator
// writes replace the credential: content type, the provider's transport
// headers, its auth headers and the key, the stream marker, and last the
// operator's overrides, from which credential and framing headers are dropped
// again here. A provider that named its own Accept keeps it: Bedrock frames a
// stream as a binary event stream, not as SSE.
func headers(h http.Header, req Request, auth *upstream.Auth) {
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
