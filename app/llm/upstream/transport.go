package upstream

import (
	"errors"
	"net/url"
	"regexp"
	"rllmx/app/protocol/cosy"
	"strings"
	"unicode"
)

const (
	maxRetryRules = 8
	maxRetryTries = 10
	maxRetryDelay = 60_000
	// maxTimeoutMs bounds a provider's own header or silence bound.
	maxTimeoutMs = 15 * 60 * 1000
	maxRegions   = 8
)

// regionID is the shape of a cluster id: a short lower-case word.
var regionID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// Placeholders a provider's address may hold; the executor fills them from the
// account's data.
const (
	AccountIDToken = "{accountId}"
	// BaseURLToken is the server address the account was connected to, for a
	// provider that runs on the user's own instance.
	BaseURLToken = "{baseUrl}"
)

// probeAddress is an address with its placeholders replaced by a host that
// parses, so the address can be checked before any account exists.
func probeAddress(addr string) string {
	addr = strings.ReplaceAll(addr, BaseURLToken, "https://x")
	return strings.ReplaceAll(addr, AccountIDToken, "x")
}

// BareOrigin reports an address that is a bare https origin: a host and an
// optional port, nothing after it. A server address that comes from a request or
// an account is held to this before anything is sent to it.
func BareOrigin(raw string) bool {
	if len(raw) > 256 || strings.ContainsFunc(raw, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil &&
		(u.Path == "" || u.Path == "/") && u.RawQuery == "" && u.Fragment == "" && !strings.HasSuffix(raw, "/")
}

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
	if err := t.validateLanes(); err != nil {
		return err
	}
	if err := t.validateHosts(); err != nil {
		return err
	}
	if t.Region != "" && !ValidRegion(t.Region) {
		return errors.New("transport.region is not an AWS region")
	}
	for _, bound := range []int{t.TimeoutMs, t.StallTimeoutMs} {
		if bound < 0 || bound > maxTimeoutMs {
			return errors.New("transport timeout is out of range")
		}
	}
	if err := t.validateRegions(); err != nil {
		return err
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
	if t.ThinkingFormat != "" && !thinkingFormats[t.ThinkingFormat] {
		return errors.New("transport.thinkingFormat is unknown")
	}
	if t.SigningKey != "" {
		// A key that cannot be parsed would fail every request it signed, so it
		// fails the load instead, where the message names the file.
		if _, err := cosy.ParseKey(t.SigningKey); err != nil {
			return errors.New("transport.signingKey is not a usable public key")
		}
	}
	return nil
}

// thinkingFormats are the shapes a provider may fix for all its models.
var thinkingFormats = map[string]bool{"openai": true, "claude-adaptive": true, "claude-budget": true, "zai": true, "kimi": true, "nous": true, "deepseek": true, "tokenrouter": true}

// maxLanes bounds the endpoints one provider may name: one per wire format.
const maxLanes = 4

// validateLanes holds each lane to the rules of the provider's own address: a
// known format, an address on the same host, plain headers, a known scheme.
// validateRegions holds each cluster to a plain id and an https host, so a
// region a connection names cannot redirect a request.
func (t *Transport) validateRegions() error {
	if len(t.Regions) > maxRegions {
		return errors.New("transport.regions has too many entries")
	}
	seen := map[string]bool{}
	for _, r := range t.Regions {
		if !regionID.MatchString(r.ID) || seen[r.ID] || !BareOrigin(r.Host) {
			return errors.New("transport.regions holds an invalid entry")
		}
		seen[r.ID] = true
	}
	if t.DefaultRegion != "" && !seen[t.DefaultRegion] {
		return errors.New("transport.defaultRegion is not one of the regions")
	}
	return nil
}

func (t *Transport) validateLanes() error {
	if len(t.Lanes) > maxLanes {
		return errors.New("transport.lanes has too many entries")
	}
	base, err := url.Parse(probeAddress(t.BaseURL))
	if err != nil {
		return errors.New("transport.baseUrl is not a plain http or https address")
	}
	for format, l := range t.Lanes {
		if !formats[format] {
			return errors.New("a transport lane has an unknown format")
		}
		u, err := url.Parse(l.BaseURL)
		if err != nil || u.Scheme != base.Scheme || !strings.EqualFold(u.Host, base.Host) || u.User != nil || u.Fragment != "" || u.RawQuery != "" {
			return errors.New("a transport lane must be a plain address on the base address's host")
		}
		if l.URLSuffix != "" && (!strings.HasPrefix(l.URLSuffix, "?") || len(l.URLSuffix) > 64 ||
			strings.ContainsFunc(l.URLSuffix, func(r rune) bool { return r <= ' ' || r == '#' || r == 0x7f })) {
			return errors.New("a transport lane urlSuffix must be a short query string")
		}
		authHeader := ""
		if l.Auth != nil {
			authHeader = l.Auth.Header
			if err := l.Auth.validate(l.BaseURL); err != nil {
				return err
			}
		}
		if len(l.Headers) > maxAuthHeaders {
			return errors.New("a transport lane has too many headers")
		}
		for name, value := range l.Headers {
			if !HeaderName(name) || ReservedHeader(name) || (authHeader != "" && strings.EqualFold(name, authHeader)) ||
				len(value) > maxHeaderBytes || strings.IndexFunc(value, unicode.IsControl) >= 0 {
				return errors.New("a transport lane has an invalid header")
			}
		}
	}
	return nil
}

// maxHosts bounds the addresses one provider may list: a failover list longer
// than this is a data error, not a deployment.
const maxHosts = 4

// validateHosts holds every extra address to the same rules as the base one: a
// plain https address, no credentials, no query, and a host of its own.
func (t *Transport) validateHosts() error {
	if len(t.BaseURLs) > maxHosts {
		return errors.New("transport.baseUrls has too many entries")
	}
	seen := map[string]bool{}
	for _, raw := range t.BaseURLs {
		u, err := url.Parse(probeAddress(raw))
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" {
			return errors.New("a transport baseUrls entry is not a plain https address")
		}
		if seen[u.Host] {
			return errors.New("transport.baseUrls repeats a host")
		}
		seen[u.Host] = true
	}
	return nil
}

// checkAddress holds the base address to a plain URL. The placeholders it may
// hold are {accountId} and, at the start, {baseUrl}; an executor that builds its
// own address has none.
func (t *Transport) checkAddress(executor string) error {
	if t.BaseURL == "" {
		if executor == ExecutorDefault {
			return errors.New("transport.baseUrl is required")
		}
		return nil
	}
	if strings.Count(t.BaseURL, BaseURLToken) > 1 || (strings.Contains(t.BaseURL, BaseURLToken) && !strings.HasPrefix(t.BaseURL, BaseURLToken)) {
		return errors.New("transport.baseUrl may hold {baseUrl} once, at its start")
	}
	probe := probeAddress(t.BaseURL)
	if strings.ContainsAny(probe, "{}") {
		return errors.New("transport.baseUrl holds an unknown placeholder")
	}
	u, err := url.Parse(probe)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return errors.New("transport.baseUrl is not a plain http or https address")
	}
	for _, other := range []string{t.ResponsesURL, t.MessagesURL} {
		if other == "" {
			continue
		}
		o, err := url.Parse(other)
		if err != nil || o.Scheme != u.Scheme || !strings.EqualFold(o.Host, u.Host) || o.User != nil || o.Fragment != "" || o.RawQuery != "" {
			return errors.New("transport.responsesUrl and messagesUrl must be plain addresses on the base address's host")
		}
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
