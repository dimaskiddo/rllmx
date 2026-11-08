package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"rllmx/app/cache"
)

const (
	signatureTTL = time.Hour
	// maxSignatureBytes bounds one stored signature.
	maxSignatureBytes = 16 << 10
)

// signatureStore keeps a provider's thought signatures between turns, so a
// client that drops one does not break the next turn. An entry is keyed by the
// API key and the call id, never the bare id, so one key cannot read or guess
// another's; and it is offered back only to a model of the family that issued
// it.
type signatureStore struct {
	cache  Loader
	keyID  string
	family string
}

func newSignatureStore(c Loader, keyID, model string) *signatureStore {
	if c == nil || keyID == "" {
		return nil
	}
	return &signatureStore{cache: c, keyID: keyID, family: modelFamily(model)}
}

func (s *signatureStore) key(callID string) string {
	sum := sha256.Sum256([]byte(s.keyID + "\x00" + callID))
	return hex.EncodeToString(sum[:])
}

// Get returns the signature stored for a call, when the family matches.
func (s *signatureStore) Get(callID string) (string, bool) {
	if s == nil || callID == "" {
		return "", false
	}
	raw, ok := s.cache.Get(cache.ToolCalls, s.key(callID))
	if !ok {
		return "", false
	}
	family, sig, found := strings.Cut(string(raw), "\x00")
	if !found || family != s.family || sig == "" {
		return "", false
	}
	return sig, true
}

// Put remembers a signature. One over the size cap, or with no call id, is not
// stored.
func (s *signatureStore) Put(callID, signature string) {
	if s == nil || callID == "" || signature == "" || len(signature) > maxSignatureBytes {
		return
	}
	s.cache.Set(cache.ToolCalls, s.key(callID), []byte(s.family+"\x00"+signature), signatureTTL)
}

// modelFamily is the model name up to its first digit group's end, lower case:
// "gemini-2.5-pro" and "gemini-2.5-flash" are one family, "gemini-3-pro" is
// another. A name with no digit is its own family.
func modelFamily(model string) string {
	m := strings.ToLower(model)
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	start := strings.IndexAny(m, "0123456789")
	if start < 0 {
		return m
	}
	end := start
	for end < len(m) && (m[end] >= '0' && m[end] <= '9' || m[end] == '.') {
		end++
	}
	return strings.TrimRight(m[:end], ".")
}
