package pipeline

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/translate"
	"rllmx/app/platform/cache"
)

const (
	// CacheHeader carries the request's wish (use, bypass) and the answer's
	// outcome (hit, miss).
	CacheHeader = "X-Rllm-Cache"

	cacheUse    = "use"
	cacheBypass = "bypass"
	cacheHit    = "hit"
	cacheMiss   = "miss"

	defaultCacheTTL     = time.Hour
	maxCacheTTL         = 7 * 24 * time.Hour
	defaultCacheEntryKB = 512
	maxCacheEntryKB     = 4096
	// maxCachedRequest is the largest request body the cache will key on.
	maxCachedRequest = 4 << 20

	projectionTTL = 30 * time.Second
	flagTTL       = 10 * time.Second
)

// keyFlags says whether an API key may use the response cache.
type keyFlags interface {
	ResponseCacheEnabled(ctx context.Context, keyID string) (bool, error)
}

// Loader is the cache's read-through load.
type Loader interface {
	Load(ctx context.Context, g cache.Group, key string, ttl time.Duration, load func(context.Context) ([]byte, error)) ([]byte, error)
	Get(g cache.Group, key string) ([]byte, bool)
	Set(g cache.Group, key string, val []byte, ttl time.Duration)
}

// ResponseCache holds the pieces the opt-in answer cache needs. A nil value is
// off. Budget bounds the in-process store and is nil when a shared store is
// used.
type ResponseCache struct {
	Cache  Loader
	Flags  keyFlags
	Budget *cache.Budget
}

// cacheSettings is the secret-free projection of the `responseCache` setting.
type cacheSettings struct {
	Enabled bool          `json:"enabled"`
	TTL     time.Duration `json:"ttl"`
	MaxKB   int           `json:"maxKb"`
}

// projectSettings reads the setting, filling what is missing or out of range
// with the defaults. Anything that is not an object is off.
func projectSettings(raw string, present bool) cacheSettings {
	s := cacheSettings{TTL: defaultCacheTTL, MaxKB: defaultCacheEntryKB}
	if !present {
		return s
	}
	v := gjson.Parse(raw)
	if !v.IsObject() {
		return cacheSettings{}
	}
	s.Enabled = v.Get("enabled").Type == gjson.True
	if t := v.Get("ttlSeconds"); t.Type == gjson.Number && t.Int() >= 1 && t.Int() <= int64(maxCacheTTL/time.Second) {
		s.TTL = time.Duration(t.Int()) * time.Second
	}
	if k := v.Get("maxEntryKb"); k.Type == gjson.Number && k.Int() >= 1 && k.Int() <= maxCacheEntryKB {
		s.MaxKB = int(k.Int())
	}
	return s
}

// settings returns the projection, through the cache so a request does not read
// the settings row.
func (c *call) cacheSettings(ctx context.Context) (cacheSettings, bool) {
	rc := c.p.d.ResponseCache
	if c.p.d.Settings == nil {
		return cacheSettings{}, false
	}
	raw, err := rc.Cache.Load(ctx, cache.Settings, "response", projectionTTL, func(lctx context.Context) ([]byte, error) {
		doc, err := c.p.d.Settings.Effective(lctx)
		if err != nil {
			return nil, err
		}
		v, ok := doc.GetRaw("responseCache")
		return json.Marshal(projectSettings(v, ok))
	})
	if err != nil {
		return cacheSettings{}, false
	}
	var s cacheSettings
	if json.Unmarshal(raw, &s) != nil || !s.Enabled {
		return cacheSettings{}, false
	}
	return s, true
}

func (c *call) keyFlag(ctx context.Context) bool {
	rc := c.p.d.ResponseCache
	raw, err := rc.Cache.Load(ctx, cache.APIKeys, "rc:"+c.in.Caller.ID, flagTTL, func(lctx context.Context) ([]byte, error) {
		on, err := rc.Flags.ResponseCacheEnabled(lctx, c.in.Caller.ID)
		if err != nil {
			return nil, err
		}
		return []byte(strconv.FormatBool(on)), nil
	})
	return err == nil && string(raw) == "true"
}

// wantsCache is true for a request the cache may answer: a non-stream call from
// a real API key that asked for it or pinned the temperature at zero.
func (c *call) wantsCache() bool {
	if c.in.Cache == cacheBypass || c.clientStream || c.in.Caller.Anonymous || c.in.Caller.ID == "" {
		return false
	}
	if len(c.in.Body) > maxCachedRequest {
		return false
	}
	if c.in.Cache == cacheUse {
		return true
	}
	t := c.req.Params.Temperature
	return t != nil && *t == 0
}

// cached answers from the cache when it can, and otherwise records the key so
// the answer is stored on its way out. It runs after the model is resolved and
// before anything is fetched, translated or sent, so a hit touches no account.
func (c *call) cached(ctx context.Context) (Output, bool) {
	rc := c.p.d.ResponseCache
	if rc == nil || rc.Cache == nil || rc.Flags == nil || !c.wantsCache() {
		return Output{}, false
	}
	s, on := c.cacheSettings(ctx)
	if !on || !c.keyFlag(ctx) {
		return Output{}, false
	}
	body, ok := canonicalBody(c.in.Body)
	if !ok {
		return Output{}, false
	}
	c.cacheKey = c.requestKey(ctx, body)
	c.cacheOpts = s
	raw, hit := rc.Cache.Get(cache.Response, c.cacheKey)
	if !hit {
		return Output{}, false
	}
	var e cachedAnswer
	if json.Unmarshal(raw, &e) != nil || len(e.Body) == 0 {
		return Output{}, false
	}
	r := &c.row
	r.Cached = true
	r.PromptTokens, r.CompletionTokens, r.CachedTokens, r.ReasoningTokens = e.Prompt, e.Completion, e.Cached, e.Reasoning
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set(CacheHeader, cacheHit)
	return Output{Status: http.StatusOK, Header: h, Body: e.Body}, true
}

// requestKey is the key of the answer to this request: who asked, in which
// format and with which command line tool, where it went and with what
// thinking, and the canonical body. The tool is part of it because a request
// from Claude Code and the same body from another client leave as different
// upstream requests. The
// thinking is the decision after the name suffix and the model's own settings,
// so a setting that changes it can never share an entry with one that does not.
func (c *call) requestKey(ctx context.Context, body []byte) string {
	t := c.tctx.Thinking
	return cacheKeyBody([]string{
		c.in.Caller.ID, c.format, c.dest.provider, c.dest.model, c.client,
		t.Mode, t.Level, strconv.Itoa(t.Budget), t.Display, c.saverKey(ctx), c.skillKey(ctx),
	}, body)
}

// cachedAnswer is what an entry holds: the client-format body and the counts
// the first call reported. It holds no request text and no credential.
type cachedAnswer struct {
	// Body is stored as the JSON it is: a byte slice would be base64 text,
	// a third larger in the budget.
	Body       json.RawMessage `json:"b"`
	Prompt     int64           `json:"p"`
	Completion int64           `json:"c"`
	Cached     int64           `json:"k"`
	Reasoning  int64           `json:"r"`
}

// storable is true for an answer the cache may keep: no tool call, in either
// the message or the finish reason.
func storable(ans *translate.Response) bool {
	return len(ans.Message.ToolCalls) == 0 && ans.FinishReason != translate.FinishToolCalls
}

// remember stores a delivered answer, if it is allowed and fits both the entry
// limit and the in-process budget. Failure to store is silent: it is a miss.
func (c *call) remember(ans *translate.Response, body []byte) {
	rc := c.p.d.ResponseCache
	if c.cacheKey == "" || !storable(ans) || len(body) > c.cacheOpts.MaxKB<<10 || len(body) > cache.MaxValueBytes {
		return
	}
	val, err := json.Marshal(cachedAnswer{
		Body: body, Prompt: ans.Usage.Prompt, Completion: ans.Usage.Completion, Cached: ans.Usage.Cached, Reasoning: ans.Usage.Reasoning,
	})
	if err != nil || len(val) > cache.MaxValueBytes || !rc.Budget.Reserve(len(val), c.cacheOpts.TTL) {
		return
	}
	rc.Cache.Set(cache.Response, c.cacheKey, val, c.cacheOpts.TTL)
}
