package pipeline

import (
	"time"

	"rllmx/app/llm/translate"
	"rllmx/app/platform/cache"
	"rllmx/app/store/dialect"
)

const (
	// replayTTL bounds how long one conversation's opening turn is kept. It is
	// the signature store's lifetime, because both answer the same question: is
	// this the same conversation.
	replayTTL = time.Hour
	// maxReplayBytes bounds one frozen turn, which is a user's own text.
	maxReplayBytes = 32 << 10
)

// replayStore keeps the opening user turn of a conversation, so a later turn
// can send it back byte for byte. Kiro caches a prompt from its first turn, so
// rebuilding the same turn with a new timestamp on every request would miss
// that cache on every request.
//
// An entry is keyed by the API key, the account and the conversation, never by
// the conversation alone: one client's opening turn is not another's.
type replayStore struct {
	cache  Loader
	keyID  string
	connID string
}

// NewReplay returns the store a request's codec reads through, or nil when
// nothing can be keyed.
func NewReplay(c Loader, keyID, connID string) translate.ReplayStore {
	if c == nil || keyID == "" {
		return nil
	}
	return &replayStore{cache: c, keyID: keyID, connID: connID}
}

// key names one conversation's opening turn under one key and account.
func (r *replayStore) key(session string) string {
	return dialect.HashKey(r.keyID, r.connID, session)
}

// Frozen returns the opening turn kept for a conversation, or nothing.
func (r *replayStore) Frozen(session string) (string, bool) {
	if r == nil || session == "" {
		return "", false
	}
	raw, ok := r.cache.Get(cache.ToolCalls, r.key(session))
	if !ok || len(raw) == 0 {
		return "", false
	}
	return string(raw), true
}

// Freeze remembers a conversation's opening turn. One over the cap, or with no
// conversation, is not stored.
func (r *replayStore) Freeze(session, turn string) {
	if r == nil || session == "" || turn == "" || len(turn) > maxReplayBytes {
		return
	}
	r.cache.Set(cache.ToolCalls, r.key(session), []byte(turn), replayTTL)
}
