package oauth

import (
	"context"
	"errors"
	"strings"
	"time"

	"rllmx/app/llm/upstream"
)

// ErrBulk means the provider does not take a pasted list of tokens.
var ErrBulk = errors.New("oauth: the provider takes no bulk token import")

// BulkLimit bounds one pasted list.
const BulkLimit = 100

// BulkItem is one account in a pasted list. The tokens are the same ones a
// login would have stored, minus anything the provider mints later.
type BulkItem struct {
	Access    string
	Refresh   string
	ID        string
	Email     string
	ExpiresIn int64
	ExpiresAt time.Time
}

// BulkOutcome is what became of one item: a connection id, or a fixed word for
// why it was refused. It never carries a token.
type BulkOutcome struct {
	ID      string
	Failure string
}

// BulkImport stores a pasted list of token accounts. A bad item fails alone: a
// rejected token does not stop the ones after it. The provider's own data must
// set `bulk`.
func (s *Service) BulkImport(ctx context.Context, provider string, items []BulkItem) ([]BulkOutcome, error) {
	f, err := s.flow(provider)
	if err != nil {
		return nil, err
	}
	if !f.Bulk {
		return nil, ErrBulk
	}
	if len(items) == 0 || len(items) > BulkLimit {
		return nil, ErrBulk
	}
	out := make([]BulkOutcome, 0, len(items))
	for _, item := range items {
		id, err := s.bulkOne(ctx, f, item)
		if err != nil {
			out = append(out, BulkOutcome{Failure: bulkFailure(err)})
			continue
		}
		out = append(out, BulkOutcome{ID: id})
	}
	return out, nil
}

// bulkOne stores one item.
func (s *Service) bulkOne(ctx context.Context, f Flow, item BulkItem) (string, error) {
	access := strings.TrimSpace(item.Access)
	if !credentialText(access) {
		return "", ErrToken
	}
	t := Tokens{Access: access, Refresh: strings.TrimSpace(item.Refresh), ID: strings.TrimSpace(item.ID), ExpiresIn: item.ExpiresIn}
	acc := Account{Email: CleanEmail(strings.TrimSpace(item.Email))}
	if !item.ExpiresAt.IsZero() {
		acc.ExpiresAt = item.ExpiresAt.UTC()
	}
	// A pasted list names its account from the tokens themselves, never from a
	// call: a bulk import makes no network request. A stated address wins.
	fromToken := claimsAccount(t)
	if fromToken.Email != "" && acc.Email == "" {
		acc.Email = fromToken.Email
	}
	acc.PSD = fromToken.PSD
	if f.Flow == upstream.FlowDevice {
		// A bulk account is one a device login would have made, and its renewal
		// kind is recorded so the refresher presents it the same way.
		acc.PSD = withPSD(acc.PSD, "authMethod", "device_code")
	}
	return s.save(ctx, f, t, acc)
}

// bulkFailure is the fixed word one refused item reports.
func bulkFailure(err error) string {
	switch {
	case errors.Is(err, ErrToken):
		return "invalid_token"
	case errors.Is(err, ErrStorage):
		return "storage"
	}
	if class := ClassOf(err); class != "" {
		return string(class)
	}
	return "rejected"
}
