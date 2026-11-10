package executor

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"rllmx/app/llm/upstream"
)

// ErrBadAccount means the account's own data is missing or malformed for what
// the provider needs, so nothing was sent. It never carries the value.
var ErrBadAccount = errors.New("executor: the account's data is missing or invalid")

// ErrAccountSession means a session the provider needs for the account itself
// could not be had, so nothing was sent. It never carries the cookie.
var ErrAccountSession = errors.New("executor: the account session could not be obtained")

// CookieSource hands an executor a session cookie for one account and cluster.
// The cookie is a credential: it is never logged and never stored in an answer.
type CookieSource interface {
	// Cookie returns the cookie header value, or ErrAccountSession when there is
	// none. fresh skips a cached session.
	Cookie(ctx context.Context, pass, userID, cUserID, region string, fresh bool) (string, error)
}

// AccountToken is what an account id, a deployment or a version may look like:
// nothing that could change the shape of an address or a header.
var AccountToken = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// fillAccount puts the account's own values into an address that asks for them:
// the account id, and the server address a login was made against.
func fillAccount(req Request) (string, error) {
	target := req.URL
	if strings.Contains(target, upstream.AccountIDToken) {
		id := ""
		if req.Account != nil {
			id = req.Account("accountId")
		}
		if !AccountToken.MatchString(id) {
			return "", ErrBadAccount
		}
		target = strings.ReplaceAll(target, upstream.AccountIDToken, id)
	}
	if strings.Contains(target, upstream.BaseURLToken) {
		base := ""
		if req.Account != nil {
			base = req.Account("baseUrl")
		}
		if !upstream.BareOrigin(base) {
			return "", ErrBadAccount
		}
		target = strings.ReplaceAll(target, upstream.BaseURLToken, base)
	}
	return target, nil
}

// retryTable is the provider's retry rules laid over the table in force.
func retryTable(base map[int]Rule, e upstream.Entry) map[int]Rule {
	if len(e.Transport.Retry) == 0 {
		return base
	}
	out := make(map[int]Rule, len(base)+len(e.Transport.Retry))
	for k, v := range base {
		out[k] = v
	}
	for status, r := range e.Transport.Retry {
		out[status] = Rule{Attempts: r.Attempts, Delay: msDuration(r.DelayMs)}
	}
	return out
}

func Field(req Request, name string) string {
	if req.Account == nil {
		return ""
	}
	return strings.TrimSpace(req.Account(name))
}
