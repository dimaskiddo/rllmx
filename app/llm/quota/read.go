package quota

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/upstream"
	"rllmx/app/llm/zed"
	"rllmx/app/platform/cache"
	"rllmx/app/platform/file"
	"rllmx/app/store/repository"
)

const (
	// callTimeout bounds one upstream call of a reading.
	callTimeout = 8 * time.Second
	// readTimeout bounds a whole reading, a renewal and the one retry included.
	readTimeout = 25 * time.Second
	// readTTL is how long a reading serves a dashboard poll.
	readTTL = 60 * time.Second
)

// Access is what one reading presents: the account's key or token, and the
// connection it was read from, which a fetcher may need a field of.
type Access struct {
	Token string
	Conn  repository.Connection
}

// Reader answers what is left of a connection's quota. It calls the provider,
// never the account selector's gate, and answers the dashboard's own shape.
type Reader struct {
	Reg    *upstream.Registry
	Client *http.Client
	Cache  *cache.Cache
	// Access resolves the credential a reading presents. It renews a token that
	// is due, so a reader never sends an expired one on purpose.
	Access func(context.Context, repository.Connection) (Access, error)
	// Renew replaces a credential the provider refused. Nil disables the retry.
	Renew func(context.Context, repository.Connection) (repository.Connection, error)
	// Zed answers the hosted service's account record, which needs its own
	// address and token cache.
	Zed *zed.Client
	Now func() time.Time
}

// Read answers one connection's quota. force skips the cached reading, for a
// dashboard that asks again right after a reset.
func (r *Reader) Read(ctx context.Context, conn repository.Connection, force bool) (Reading, error) {
	f, ok := fetchers[conn.Provider]
	if !ok {
		return Reading{}, ErrUnsupported
	}
	if !Serves(conn.Provider, conn.AuthType) {
		return Reading{}, ErrNotEligible
	}
	if r.Cache == nil {
		return r.read(ctx, conn, f)
	}
	if !force {
		if raw, hit := r.Cache.Get(cache.Quota, conn.ID); hit {
			var out Reading
			if json.Unmarshal(raw, &out) == nil {
				return out, nil
			}
		}
	}
	out, err := r.read(ctx, conn, f)
	// A reading that reports a failure is asked again next time: the provider
	// may be back, and a stale message would hide it for the whole ttl.
	if err == nil && out.Message == "" {
		if raw, mErr := json.Marshal(out); mErr == nil {
			r.Cache.Set(cache.Quota, conn.ID, raw, readTTL)
		}
	}
	return out, err
}

// read runs one fetcher, renewing the credential once when the provider refused
// it. The refusal is the fetcher's own typed answer, never a guess from a
// message.
func (r *Reader) read(ctx context.Context, conn repository.Connection, f fetcher) (Reading, error) {
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	entry, _ := r.entry(conn.Provider)
	access, err := r.access(ctx, conn)
	if err != nil {
		return Reading{}, err
	}
	out, err := f.run(ctx, r, entry, access)
	if !errors.Is(err, ErrAuth) || r.Renew == nil {
		return out, err
	}
	fresh, rerr := r.Renew(ctx, conn)
	if rerr != nil {
		return Reading{}, fmt.Errorf("%w: %v", ErrExpired, rerr)
	}
	if access, err = r.access(ctx, fresh); err != nil {
		return Reading{}, err
	}
	return f.run(ctx, r, entry, access)
}

func (r *Reader) access(ctx context.Context, conn repository.Connection) (Access, error) {
	if r.Access == nil {
		return Access{}, ErrNotEligible
	}
	a, err := r.Access(ctx, conn)
	if err != nil {
		return Access{}, err
	}
	if a.Token == "" {
		return Access{}, ErrNotEligible
	}
	return a, nil
}

// entry reads the provider's registry row, which holds the identity headers and
// the addresses a fetcher shares with the chat path.
func (r *Reader) entry(provider string) (upstream.Entry, bool) {
	if r.Reg == nil {
		return upstream.Entry{}, false
	}
	if id, ok := r.Reg.Resolve(provider); ok {
		return r.Reg.ByID(id)
	}
	return upstream.Entry{}, false
}

func (r *Reader) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// request is one upstream call of a reading.
type request struct {
	Method  string
	URL     string
	Headers map[string]string
	Body    []byte
}

// do sends one call and returns its status and its body, both bounded. The
// caller decides what a status means.
func (r *Reader) do(ctx context.Context, q request) (int, []byte, error) {
	code, _, raw, err := r.doFull(ctx, q)
	return code, raw, err
}

// doFull also returns the answer's headers, for a provider that reports its
// limits there.
func (r *Reader) doFull(ctx context.Context, q request) (int, http.Header, []byte, error) {
	if r.Client == nil {
		return 0, nil, nil, errors.New("no outbound client")
	}
	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	var body io.Reader
	if q.Body != nil {
		body = bytes.NewReader(q.Body)
	}
	method := q.Method
	if method == "" {
		method = http.MethodGet
	}
	req, err := http.NewRequestWithContext(callCtx, method, q.URL, body)
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("Accept", "application/json")
	if q.Body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range q.Headers {
		req.Header.Set(k, v)
	}
	resp, err := r.Client.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	raw, err := file.ReadCapped(resp.Body, maxAnswer)
	if err != nil {
		return resp.StatusCode, resp.Header, nil, err
	}
	return resp.StatusCode, resp.Header, raw, nil
}

// answered turns a refusal into the retryable error and any other bad status
// into a soft message. Neither carries the provider's body: it may echo the
// credential or a page of markup.
func answered(provider string, code int) (Reading, error) {
	if code == http.StatusUnauthorized || code == http.StatusForbidden {
		return Reading{}, ErrAuth
	}
	return Reading{Message: fmt.Sprintf("%s answered %d", provider, code)}, nil
}

// failed is a soft message for a call that never got an answer.
func failed(provider string, err error) (Reading, error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return Reading{Message: fmt.Sprintf("%s did not answer in time", provider)}, nil
	}
	return Reading{Message: fmt.Sprintf("%s could not be reached", provider)}, nil
}

// identity merges the static headers a provider's data file gives a request: the
// transport's, then the login's, with the transport winning on a shared name.
func identity(e upstream.Entry) map[string]string {
	h := map[string]string{}
	if e.OAuth != nil {
		for k, v := range e.OAuth.Headers {
			h[k] = v
		}
		for k, v := range e.OAuth.Identity {
			if _, ok := h[k]; !ok {
				h[k] = v
			}
		}
	}
	for k, v := range e.Transport.Headers {
		h[k] = v
	}
	return h
}

// bearer adds the token to a call's headers the way a login's request carries it.
func bearer(h map[string]string, token string) map[string]string {
	if h == nil {
		h = map[string]string{}
	}
	h["Authorization"] = "Bearer " + token
	return h
}

// keyed adds the token the way the provider checks a pasted key, from its data
// file: the scheme, the header and the static headers all come from there.
func keyed(e upstream.Entry, h map[string]string, token string) map[string]string {
	out := h
	if e.Auth != nil {
		for k, v := range e.Auth.RequestHeaders(token) {
			out[k] = v
		}
		return out
	}
	return bearer(out, token)
}

// atof reads a number given as text, as a rate-limit header is.
func atof(s string) (float64, bool) {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}

// stored reads one field a connection carries, provider specific data first, as
// a login's account header does.
func stored(conn repository.Connection, name string) string {
	d := conn.Doc()
	if psd, _ := d.GetRaw("providerSpecificData"); psd != "" {
		if v := gjson.Get(psd, name).String(); v != "" {
			return v
		}
	}
	v, _ := d.GetString(name)
	return v
}
