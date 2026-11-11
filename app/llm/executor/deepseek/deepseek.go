// Package deepseek sends a turn to DeepSeek's web endpoint, which takes one
// prompt string, trades the pasted user token for a short-lived access token,
// and guards the completion with a proof of work.
package deepseek

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/executor"
	"rllmx/app/platform/cache"
	"rllmx/app/store/dialect"
)

// Executor is the web endpoint's executor. It holds no per-account state: the
// access token and a persistent session live in the memory-only cache.
type Executor struct {
	Default *executor.Default
	// Cache keeps the access token and, for an account that asks for one, the
	// session id. Nil means every call trades a token and opens a session.
	Cache *cache.Cache
	// Now is the clock; nil uses time.Now.
	Now func() time.Time
}

// The endpoint's paths, relative to the provider's own origin.
const (
	tokenPath      = "/api/v0/users/current"
	sessionNewPath = "/api/v0/chat_session/create"
	sessionDelPath = "/api/v0/chat_session/delete"
	powPath        = "/api/v0/chat/create_pow_challenge"
	completionPath = "/api/v0/chat/completion"
)

// Lifetimes of what the cache holds.
const (
	tokenLife   = time.Hour
	sessionLife = 30 * time.Minute
	// maxControlBody bounds a small answer from the service.
	maxControlBody = 1 << 20
)

// The identity the web client presents. Every value is a fixed word: nothing
// about the machine or the operator goes on the wire (D268).
const (
	clientBundle = "com.deepseek.chat"
	clientLocale = "en-US"
	clientPlat   = "web"
	clientVer    = "2.0.0"
	clientAgent  = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/149.0.0.0 Safari/537.36"
)

// ErrToken means the pasted user token is not one the service accepts. It is
// the account's own problem: nothing is retried and no other account is tried.
var ErrToken = executor.ErrBadAccount

// plan is what one request asks for, computed once: the prompt to send, and the
// tool contract when the request offers tools.
type plan struct {
	prompt   string
	hasTools bool
	nonce    string
	names    map[string]bool
}

// Execute sends one turn.
func (e *Executor) Execute(ctx context.Context, req executor.Request) (executor.Response, error) {
	if req.Key == "" {
		return executor.Response{}, executor.ErrNoCredential
	}
	base, ok := origin(req.URL)
	if !ok {
		return executor.Response{}, executor.ErrUnreachable
	}
	body := gjson.ParseBytes(req.Body)
	if !body.IsObject() {
		return executor.Response{}, executor.ErrBadAccount
	}
	token, err := e.accessToken(ctx, req, base)
	if err != nil {
		return executor.Response{}, err
	}
	pl := buildPlan(req, body)
	persist := strings.EqualFold(strings.TrimSpace(executor.Field(req, "persistSession")), "true")
	session, reused, err := e.session(ctx, req, base, token, persist)
	if err != nil {
		e.dropToken(ctx, req)
		return executor.Response{}, err
	}
	resp, err := e.complete(ctx, req, base, token, session, body, persist, pl)
	if err != nil {
		e.dropSession(ctx, req)
		return executor.Response{}, err
	}
	if persist && reused && resp.Status >= http.StatusBadRequest {
		// A reused session the service no longer knows: open a fresh one once.
		executor.Drain(resp.Body)
		_ = resp.Body.Close()
		e.dropSession(ctx, req)
		session, _, err = e.session(ctx, req, base, token, false)
		if err != nil {
			e.dropToken(ctx, req)
			return executor.Response{}, err
		}
		resp, err = e.complete(ctx, req, base, token, session, body, false, pl)
		if err != nil {
			return executor.Response{}, err
		}
	}
	if !persist || resp.Status >= http.StatusBadRequest {
		// The session answered what it was asked; a fresh one is opened next time.
		e.closeSession(ctx, base, token, session)
	}
	out, err := e.reply(resp, req, body, base, pl)
	if out.Status == http.StatusUnauthorized || out.Status == http.StatusForbidden {
		e.dropToken(ctx, req)
	}
	return out, err
}

// reply turns the service's answer into a chat stream. A refusal keeps its
// status so the pipeline's account rules see it.
func (e *Executor) reply(resp executor.Response, req executor.Request, body gjson.Result, base string, pl plan) (executor.Response, error) {
	if resp.Status < 200 || resp.Status > 299 {
		return resp, nil
	}
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "application/json") {
		// A 200 carrying a refusal, which the service does send.
		raw := executor.FailedBody(resp)
		if status, refused := refusal(raw); refused {
			return executor.ReplyWith(resp, status, raw, "application/json"), nil
		}
		return executor.ReplyWith(resp, http.StatusBadGateway, []byte(`{"error":{"message":"The provider did not send a stream","type":"api_error"}}`), "application/json"), nil
	}
	t := &turn{
		src:      bufio.NewReaderSize(resp.Body, maxLine),
		model:    req.Model,
		thinking: optionsOf(req.Model, body).thinking,
		search:   optionsOf(req.Model, body).search,
		quiet:    strings.Contains(strings.ToLower(req.Model), "search-silent"),
		now:      e.now().Unix(),
		nextID:   "chatcmpl-" + dialect.NewID(),
	}
	if pl.hasTools {
		// A tool call is read out of the whole answer, so this path buffers it.
		_, _ = io.Copy(io.Discard, t)
		calls, text := parseCalls(t.text.String(), pl.nonce, pl.names)
		return executor.Response{Status: http.StatusOK, Body: io.NopCloser(strings.NewReader(synthesize(req.Model, text, calls, t.now, t.nextID))),
			Header: executor.StreamHeader(resp.Header)}, nil
	}
	return executor.Response{Status: http.StatusOK, Body: closer{t, resp.Body}, Header: executor.StreamHeader(resp.Header)}, nil
}

// closer reads the turn and closes the service's body with it.
type closer struct {
	*turn
	body io.Closer
}

func (c closer) Close() error {
	err := c.turn.Close()
	_ = c.body.Close()
	return err
}

// accessToken returns the access token for the account, trading the pasted one
// when the cache has none.
func (e *Executor) accessToken(ctx context.Context, req executor.Request, base string) (string, error) {
	key := dialect.HashKey("deepseek-web", req.Key)
	if e.Cache != nil {
		if raw, err := e.Cache.Load(ctx, cache.Connections, key, tokenLife, func(ctx context.Context) ([]byte, error) {
			return e.tradeToken(ctx, req, base)
		}); err == nil && len(raw) > 0 {
			return string(raw), nil
		}
	}
	raw, err := e.tradeToken(ctx, req, base)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// tradeToken exchanges the pasted user token for an access token.
func (e *Executor) tradeToken(ctx context.Context, req executor.Request, base string) ([]byte, error) {
	resp, err := e.get(ctx, base, tokenPath, "Bearer "+req.Key, nil, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.Status == http.StatusUnauthorized || resp.Status == http.StatusForbidden {
		executor.Drain(resp.Body)
		return nil, ErrToken
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxControlBody))
	if err != nil {
		return nil, executor.ErrUnreachable
	}
	if resp.Status < 200 || resp.Status > 299 {
		return nil, executor.ErrUnreachable
	}
	token := gjson.GetBytes(raw, "data.biz_data.token").String()
	if token == "" {
		token = gjson.GetBytes(raw, "biz_data.token").String()
	}
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return nil, ErrToken
	}
	return []byte(token), nil
}

// dropToken forgets a cached access token the service refused.
func (e *Executor) dropToken(ctx context.Context, req executor.Request) {
	if e.Cache == nil {
		return
	}
	e.Cache.Delete(cache.Connections, dialect.HashKey("deepseek-web", req.Key))
}

// session returns the session id a completion runs in, opening one when the
// account keeps none.
func (e *Executor) session(ctx context.Context, req executor.Request, base, token string, persist bool) (id string, reused bool, err error) {
	key := dialect.HashKey("deepseek-web-session", req.Key)
	if persist && e.Cache != nil {
		if raw, err := e.Cache.Load(ctx, cache.Connections, key, sessionLife, func(ctx context.Context) ([]byte, error) {
			return e.openSession(ctx, req, base, token)
		}); err == nil && len(raw) > 0 {
			return string(raw), true, nil
		}
	}
	raw, err := e.openSession(ctx, req, base, token)
	if err != nil {
		return "", false, err
	}
	return string(raw), false, nil
}

// openSession asks the service for a session id.
func (e *Executor) openSession(ctx context.Context, req executor.Request, base, token string) ([]byte, error) {
	resp, err := e.get(ctx, base, sessionNewPath, "Bearer "+token, []byte("{}"), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxControlBody))
	if err != nil {
		return nil, executor.ErrUnreachable
	}
	if resp.Status == http.StatusUnauthorized || resp.Status == http.StatusForbidden {
		return nil, ErrToken
	}
	if resp.Status < 200 || resp.Status > 299 {
		return nil, executor.ErrUnreachable
	}
	id := gjson.GetBytes(raw, "data.biz_data.chat_session.id").String()
	if id == "" {
		id = gjson.GetBytes(raw, "biz_data.chat_session.id").String()
	}
	if id == "" || strings.ContainsAny(id, " \t\r\n") {
		return nil, executor.ErrUnreachable
	}
	return []byte(id), nil
}

// dropSession forgets a cached session id.
func (e *Executor) dropSession(ctx context.Context, req executor.Request) {
	if e.Cache == nil {
		return
	}
	e.Cache.Delete(cache.Connections, dialect.HashKey("deepseek-web-session", req.Key))
}

// closeSession deletes a session the router opened and does not keep. It is
// best effort: a session left behind expires on the service's own side.
func (e *Executor) closeSession(ctx context.Context, base, token, id string) {
	body := []byte(`{"chat_session_id":` + strconv.Quote(id) + `}`)
	resp, err := e.get(ctx, base, sessionDelPath, "Bearer "+token, body, nil)
	if err != nil {
		return
	}
	executor.Drain(resp.Body)
	_ = resp.Body.Close()
}

// complete sends the turn and returns the service's answer unread.
func (e *Executor) complete(ctx context.Context, req executor.Request, base, token, session string, body gjson.Result, persist bool, pl plan) (executor.Response, error) {
	c, err := e.challenge(ctx, req, base, token)
	if err != nil {
		return executor.Response{}, err
	}
	nonce, ok := solve(ctx, c)
	if !ok {
		return executor.Response{}, executor.ErrUnreachable
	}
	proof, err := answer(c, nonce)
	if err != nil {
		return executor.Response{}, err
	}
	o := optionsOf(req.Model, body)
	payload, err := json.Marshal(map[string]any{
		"chat_session_id":   session,
		"parent_message_id": nil,
		"model_type":        o.modelType,
		"prompt":            pl.prompt,
		"ref_file_ids":      []string{},
		"thinking_enabled":  o.thinking,
		"search_enabled":    o.search,
		"preempt":           false,
	})
	if err != nil {
		return executor.Response{}, executor.ErrUnreachable
	}
	extra := map[string]string{powHeader: proof, "X-Client-Timezone-Offset": "0"}
	return e.get(ctx, base, completionPath, "Bearer "+token, payload, extra)
}

// challenge asks for one proof-of-work challenge.
func (e *Executor) challenge(ctx context.Context, req executor.Request, base, token string) (challenge, error) {
	body := []byte(`{"target_path":` + strconv.Quote(completionPath) + `}`)
	resp, err := e.get(ctx, base, powPath, "Bearer "+token, body, nil)
	if err != nil {
		return challenge{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxControlBody))
	if err != nil {
		return challenge{}, executor.ErrUnreachable
	}
	if resp.Status == http.StatusUnauthorized || resp.Status == http.StatusForbidden {
		return challenge{}, ErrToken
	}
	c, ok := parseChallenge(raw)
	if resp.Status < 200 || resp.Status > 299 || !ok {
		return challenge{}, executor.ErrUnreachable
	}
	return c, nil
}

// get makes one call to the service with the web client's fixed headers.
func (e *Executor) get(ctx context.Context, base, path, auth string, body []byte, extra map[string]string) (executor.Response, error) {
	target := base + path
	var reader io.Reader
	if body != nil {
		reader = strings.NewReader(string(body))
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, target, reader)
	if err != nil {
		return executor.Response{}, executor.ErrUnreachable
	}
	if body == nil {
		hr.Method = http.MethodGet
	}
	for k, v := range headers(base, auth, extra) {
		hr.Header.Set(k, v)
	}
	client := *e.Default.Client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(hr)
	if err != nil {
		return executor.Response{}, executor.ErrUnreachable
	}
	return executor.Response{Status: resp.StatusCode, Header: resp.Header, Body: resp.Body}, nil
}

// headers are what every call to the web endpoint presents.
func headers(base, auth string, extra map[string]string) map[string]string {
	h := map[string]string{
		"Accept":             "*/*",
		"Accept-Language":    "en-US,en;q=0.9",
		"Origin":             "https://chat.deepseek.com",
		"Referer":            "https://chat.deepseek.com/",
		"User-Agent":         clientAgent,
		"X-Client-Bundle-Id": clientBundle,
		"X-Client-Locale":    clientLocale,
		"X-Client-Platform":  clientPlat,
		"X-Client-Version":   clientVer,
		"Authorization":      auth,
	}
	for k, v := range extra {
		h[k] = v
	}
	return h
}

// refusal maps a 200 answer that carries an error code to the status it stands
// for: the token is dead, the account is rate limited, or the service failed.
func refusal(raw []byte) (int, bool) {
	code := gjson.GetBytes(raw, "code")
	if code.Type != gjson.Number || code.Int() == 0 {
		return 0, false
	}
	switch code.Int() {
	case 40003:
		return http.StatusUnauthorized, true
	case 40002:
		return http.StatusTooManyRequests, true
	}
	return http.StatusBadGateway, true
}

// origin is the scheme and host of the provider's address.
func origin(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", false
	}
	if u.Scheme != "https" {
		return "", false
	}
	return u.Scheme + "://" + u.Host, true
}

// now is the clock, or the real one.
func (e *Executor) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}
