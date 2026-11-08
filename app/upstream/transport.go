package upstream

import (
	"errors"
	"net/url"
	"strings"
	"unicode"
)

const (
	maxRetryRules = 8
	maxRetryTries = 10
	maxRetryDelay = 60_000
)

// AccountIDToken is the one placeholder a provider's address may hold; the
// executor fills it from the account's data.
const AccountIDToken = "{accountId}"

// validate checks the transport of one provider. authHeader is the credential
// header of its auth block, which a static header must not shadow.
func (t *Transport) validate(executor, authHeader string) error {
	if err := t.checkAddress(executor); err != nil {
		return err
	}
	if len(t.Headers) > maxAuthHeaders {
		return errors.New("transport.headers has too many entries")
	}
	for name, value := range t.Headers {
		switch {
		case !HeaderName(name):
			return errors.New("transport.headers has an invalid name")
		case ReservedHeader(name) || (authHeader != "" && strings.EqualFold(name, authHeader)):
			return errors.New("transport.headers names a credential or framing header")
		case len(value) > maxHeaderBytes || strings.IndexFunc(value, unicode.IsControl) >= 0:
			return errors.New("transport.headers has an invalid value")
		}
	}
	if t.URLSuffix != "" && (!strings.HasPrefix(t.URLSuffix, "?") || len(t.URLSuffix) > 64 ||
		strings.ContainsFunc(t.URLSuffix, func(r rune) bool { return r <= ' ' || r == '#' || r == 0x7f })) {
		return errors.New("transport.urlSuffix must be a short query string")
	}
	if len(t.Retry) > maxRetryRules {
		return errors.New("transport.retry has too many entries")
	}
	for status, r := range t.Retry {
		if status < 400 || status > 599 || r.Attempts < 0 || r.Attempts > maxRetryTries || r.DelayMs < 0 || r.DelayMs > maxRetryDelay {
			return errors.New("transport.retry is out of range")
		}
	}
	switch t.ReasoningInject {
	case "", ReasoningAll, ReasoningToolCalls:
	default:
		return errors.New("transport.reasoningInject is unknown")
	}
	return nil
}

// checkAddress holds the base address to a plain URL. The one placeholder it
// may hold is {accountId}; an executor that builds its own address has none.
func (t *Transport) checkAddress(executor string) error {
	if t.BaseURL == "" {
		if executor == ExecutorDefault {
			return errors.New("transport.baseUrl is required")
		}
		return nil
	}
	probe := strings.ReplaceAll(t.BaseURL, AccountIDToken, "x")
	if strings.ContainsAny(probe, "{}") {
		return errors.New("transport.baseUrl holds an unknown placeholder")
	}
	u, err := url.Parse(probe)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return errors.New("transport.baseUrl is not a plain http or https address")
	}
	return nil
}

// ModelReasoningScope is the injection scope a model family needs when its
// provider names none: Kimi wants it on tool turns, DeepSeek on every turn.
func ModelReasoningScope(model string) string {
	m := strings.ToLower(model)
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	switch {
	case strings.HasPrefix(m, "kimi-"):
		return ReasoningToolCalls
	case strings.Contains(m, "deepseek"):
		return ReasoningAll
	}
	return ""
}
