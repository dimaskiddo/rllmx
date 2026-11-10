package oauth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/goravel/framework/contracts/log"
	"github.com/tidwall/gjson"

	"rllmx/app/llm/executor"
	"rllmx/app/llm/executor/freebuff"
	"rllmx/app/platform/cache"
	"rllmx/app/platform/file"
	"rllmx/app/store/dialect"
)

// Freebuff's session lifetimes. A session binds the account to one model, so a
// claim for another is refused by the service.
const (
	freebuffSessionTTL = time.Hour
	// freebuffPollClamp bounds how long a login waits for the browser.
	freebuffPollClamp = 300 * time.Second
)

// freebuffOrigin is the host that serves both the login and the session call.
const freebuffOrigin = "https://freebuff.com"

var (
	// ErrFreebuffSession means the service refused the session, or refused the
	// account. It never carries the service's text.
	ErrFreebuffSession = errors.New("oauth: the freebuff session was refused")
	// ErrFreebuffLocked means the account's session is held by another model.
	ErrFreebuffLocked = errors.New("oauth: the freebuff session is locked to another model")
)

// FreebuffSessions claims a session for one account and model. One claim is in
// flight per account and model however many requests ask, and the result is kept
// in the memory-only cache for as long as the service said.
type FreebuffSessions struct {
	HTTP  *http.Client
	Cache *cache.Cache
	Log   log.Log
}

var _ freebuff.SessionSource = (*FreebuffSessions)(nil)

// Claim returns the session's instance id, or "" when the service grants none.
func (s *FreebuffSessions) Claim(ctx context.Context, token, model string, fresh bool) (string, error) {
	if token == "" {
		return "", executor.ErrNoCredential
	}
	key := "freebuff:" + dialect.HashKey(token, model)
	if fresh && s.Cache != nil {
		s.Cache.Delete(cache.Connections, key)
	}
	load := func(ctx context.Context) ([]byte, error) {
		return s.claim(ctx, token, model)
	}
	var raw []byte
	var err error
	if s.Cache != nil {
		raw, err = s.Cache.Load(ctx, cache.Connections, key, freebuffSessionTTL, load)
	} else {
		raw, err = load(ctx)
	}
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// claim asks the service for one session. "none" is a granted call without one;
// any other state is refused with a fixed word.
func (s *FreebuffSessions) claim(ctx context.Context, token, model string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, freebuffOrigin+"/api/v1/freebuff/session", nil)
	if err != nil {
		return nil, executor.ErrAccountSession
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", freebuffUserAgent)
	req.Header.Set("x-freebuff-model", model)
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return nil, executor.ErrAccountSession
	}
	defer resp.Body.Close()
	raw := file.ReadUpTo(resp.Body, 1<<20)
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, executor.ErrAccountSession
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, executor.ErrAccountSession
	}
	switch gjson.GetBytes(raw, "status").String() {
	case "none":
		return []byte(""), nil
	case "active":
		id := gjson.GetBytes(raw, "instanceId").String()
		if id == "" || len(id) > 256 || strings.ContainsFunc(id, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			return nil, executor.ErrAccountSession
		}
		return []byte(id), nil
	case "model_locked":
		return nil, ErrFreebuffLocked
	}
	return nil, executor.ErrAccountSession
}

// freebuffUserAgent is the client the service expects.
const freebuffUserAgent = "codebuff-cli/0.0.138"
