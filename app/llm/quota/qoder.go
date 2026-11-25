package quota

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/qoder"
	"rllmx/app/llm/upstream"
)

// qoderJob is a personal token's exchanged job token and when it stops being
// usable; the exchange is repeated shortly before that.
type qoderJob struct {
	token string
	until time.Time
}

// readQoder reads one account's quota. A device login presents a bearer token;
// a pasted personal token is exchanged for a job token first.
func readQoder(ctx context.Context, r *Reader, e upstream.Entry, a Access) (Reading, error) {
	base := qoder.OpenAPI[a.Conn.Provider]
	if base == "" {
		if url := e.OAuth.Endpoints[upstream.EndpointUser]; url != "" {
			base = strings.TrimSuffix(url, "/")
		}
	}
	if base == "" {
		return Reading{}, ErrUnsupported
	}
	token := a.Token
	if strings.HasPrefix(token, "pt-") {
		job, err := r.qoderJobToken(ctx, base, token)
		if err != nil {
			return failed("Qoder", err)
		}
		token = job
	}
	code, raw, err := r.do(ctx, request{URL: base + "/api/v2/quota/usage", Headers: map[string]string{
		"Authorization": bearerWord(token),
	}})
	if err != nil {
		return failed("Qoder", err)
	}
	if code == http.StatusUnauthorized || code == http.StatusForbidden {
		return Reading{}, ErrAuth
	}
	if code != http.StatusOK {
		return answered("Qoder", code)
	}
	return parseQoder(raw), nil
}

// qoderJobToken exchanges a personal token once and keeps the answer until
// shortly before it expires.
func (r *Reader) qoderJobToken(ctx context.Context, base, personal string) (string, error) {
	key := base + "|" + personal
	r.qoderMu.Lock()
	if job, ok := r.qoderJobs[key]; ok && job.until.After(r.now().Add(5*time.Minute)) {
		r.qoderMu.Unlock()
		return job.token, nil
	}
	r.qoderMu.Unlock()
	token, until, err := qoder.Exchange(ctx, func(ctx context.Context, method, url string, body []byte, headers map[string]string) (int, []byte, error) {
		return r.do(ctx, request{Method: method, URL: url, Body: body, Headers: headers})
	}, base, personal)
	if err != nil {
		if errors.Is(err, qoder.ErrRefused) {
			return "", fmt.Errorf("%w: the personal token was refused", ErrAuth)
		}
		return "", err
	}
	r.qoderMu.Lock()
	if r.qoderJobs == nil {
		r.qoderJobs = map[string]qoderJob{}
	}
	r.qoderJobs[key] = qoderJob{token: token, until: until}
	r.qoderMu.Unlock()
	return token, nil
}

// parseQoder turns the account's pools into rows. A pool whose total is zero is
// left out; an expiry in year 9999 means the pool never expires.
func parseQoder(raw []byte) Reading {
	out := Reading{Quotas: map[string]Quota{}}
	data := gjson.ParseBytes(raw)
	expires := data.Get("expiresAt").Int()
	var reset *time.Time
	if expires > 0 && expires != 253402214400000 {
		reset = stamp(time.UnixMilli(expires))
	}
	for _, pool := range []struct{ field, label string }{
		{"userQuota", "user"}, {"orgResourcePackage", "organization"}, {"addOnQuota", "addOn"},
	} {
		block := data.Get(pool.field)
		if !block.IsObject() {
			continue
		}
		total := block.Get("total").Float()
		if total <= 0 {
			continue
		}
		used := block.Get("used").Float()
		if remaining := block.Get("remaining"); remaining.Exists() {
			used = total - remaining.Float()
		}
		q := window(used, total, time.Time{})
		if reset != nil {
			q.ResetAt = reset
		}
		q.Unit = block.Get("unit").String()
		if q.Unit == "" {
			q.Unit = "credits"
		}
		q.DisplayName = pool.label
		out.Quotas[pool.label] = q
	}
	if len(out.Quotas) == 0 {
		out.Message = "Qoder reported no quota"
	}
	return out
}
