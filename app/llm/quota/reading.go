package quota

import (
	"errors"
	"math"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/platform/text"
)

// Errors a reading reports instead of a quota. They let the route answer a
// refusal without reading a message.
var (
	// ErrUnsupported is a provider this build cannot read.
	ErrUnsupported = errors.New("usage is not implemented for this provider")
	// ErrNotEligible is a connection whose auth type the provider's reader does
	// not serve: a key on a login-only provider, or the reverse.
	ErrNotEligible = errors.New("usage is not available for this connection")
	// ErrAuth is a refusal by the provider that a renewed token may fix.
	ErrAuth = errors.New("the provider refused the credential")
	// ErrExpired is a connection whose token cannot be renewed.
	ErrExpired = errors.New("the credential is expired")
	// ErrRefused is a provider that answered a request it understood with a
	// status the caller reports as a failure.
	ErrRefused = errors.New("the provider refused the request")
	// ErrInvalidGrant is a reset grant id that the service would not take.
	ErrInvalidGrant = errors.New("invalid reset grant id")
)

// Reading is one account's quota as the dashboard shows it. One field carries
// one unit: a window counts in its provider's own numbers, a balance in its
// currency, and the share left is always a percentage.
type Reading struct {
	Plan         string           `json:"plan,omitempty"`
	Quotas       map[string]Quota `json:"quotas"`
	Message      string           `json:"message,omitempty"`
	ResetCredits *ResetCredits    `json:"resetCredits,omitempty"`
}

// Quota is one limit of an account: a window that refills, or a balance that
// does not.
type Quota struct {
	Used                float64    `json:"used"`
	Total               float64    `json:"total"`
	RemainingPercentage float64    `json:"remainingPercentage"`
	ResetAt             *time.Time `json:"resetAt,omitempty"`
	Unlimited           bool       `json:"unlimited,omitempty"`
	Recurring           bool       `json:"recurring,omitempty"`
	// Credit marks a balance, whose Total is money or credits rather than a
	// count of requests.
	Credit      bool   `json:"isCreditBalance,omitempty"`
	Currency    string `json:"currency,omitempty"`
	Unit        string `json:"unit,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
}

// ResetCredits is what an account may still spend to refill its limits: one
// entry for each grant, or a count for a provider that lists none.
type ResetCredits struct {
	AvailableCount int      `json:"availableCount"`
	Credits        []Credit `json:"credits,omitempty"`
}

// Credit is one refill an account holds.
type Credit struct {
	Status    string     `json:"status,omitempty"`
	GrantedAt *time.Time `json:"grantedAt,omitempty"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
}

// ResetResult is the outcome of spending one refill, as the route answers it.
type ResetResult struct {
	OK         bool   `json:"ok"`
	Status     int    `json:"status"`
	Result     string `json:"result,omitempty"`
	Reason     string `json:"reason,omitempty"`
	ResetsLeft int    `json:"resetsLeft"`
	Message    string `json:"message,omitempty"`
}

// ConsumeResult is the outcome of spending one Codex credit.
type ConsumeResult struct {
	// Status is the upstream answer's status, which the route reports when the
	// credit was not spent.
	Status          int     `json:"-"`
	Code            string  `json:"code,omitempty"`
	Reset           bool    `json:"reset"`
	WindowsReset    int     `json:"windows_reset"`
	RedeemRequestID string  `json:"redeemRequestId,omitempty"`
	Credit          *Credit `json:"credit,omitempty"`
	Message         string  `json:"message,omitempty"`
}

// stampOf reads a reset time given as an instant or as a bare date.
func stampOf(v gjson.Result) time.Time {
	if t := ParseReset(v); !t.IsZero() {
		return t
	}
	if v.Type != gjson.String {
		return time.Time{}
	}
	for _, layout := range []string{"2006-01-02", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, v.String()); err == nil {
			return t
		}
	}
	return time.Time{}
}

// stamp narrows a time to the wire form the dashboard reads, and drops a zero
// one.
func stamp(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

// clampPercent keeps a share inside 0..100.
func clampPercent(f float64) float64 {
	switch {
	case math.IsNaN(f) || f < 0:
		return 0
	case f > 100:
		return 100
	}
	return f
}

// window builds a row from a count and its cap.
func window(used, total float64, reset time.Time) Quota {
	q := Quota{Used: used, Total: total, ResetAt: stamp(reset)}
	if total <= 0 {
		q.Unlimited = true
		q.RemainingPercentage = 100
		return q
	}
	q.RemainingPercentage = math.Round(clampPercent((total-used)/total*100)*100) / 100
	return q
}

// share builds a row from a used percentage of 100, which is how most providers
// report a window.
func share(used float64, reset time.Time) Quota {
	return window(clampPercent(used), 100, reset)
}

// balance builds a row for money or credits, which refills only by a top-up.
func balance(total float64, unit, currency string) Quota {
	return Quota{Total: total, RemainingPercentage: 100, Credit: true, Unit: unit, Currency: currency}
}

// maxUpstreamText bounds a sentence taken from a provider's answer before the
// dashboard shows it.
const maxUpstreamText = 200

func upstreamText(s string) string { return text.CutBytes(s, maxUpstreamText) }
