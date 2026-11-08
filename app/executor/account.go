package executor

import (
	"errors"
	"regexp"
	"strings"

	"rllmx/app/upstream"
)

// ErrBadAccount means the account's own data is missing or malformed for what
// the provider needs, so nothing was sent. It never carries the value.
var ErrBadAccount = errors.New("executor: the account's data is missing or invalid")

// accountToken is what an account id, a deployment or a version may look like:
// nothing that could change the shape of an address or a header.
var accountToken = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// fillAccount puts the account id into an address that asks for it.
func fillAccount(req Request) (string, error) {
	if !strings.Contains(req.URL, upstream.AccountIDToken) {
		return req.URL, nil
	}
	id := ""
	if req.Account != nil {
		id = req.Account("accountId")
	}
	if !accountToken.MatchString(id) {
		return "", ErrBadAccount
	}
	return strings.ReplaceAll(req.URL, upstream.AccountIDToken, id), nil
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
