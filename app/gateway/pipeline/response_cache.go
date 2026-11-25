package pipeline

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"rllmx/app/llm/translate"
	"rllmx/app/platform/cache"
)

const (
	// CacheHeader carries the request's wish (use, bypass) and the answer's
	// outcome (hit, miss).
	CacheHeader = "X-Rllm-Cache"
	// CacheSimilarity carries the score of a semantic hit.
	CacheSimilarity = "X-Rllm-Cache-Similarity"

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
	// Semantic matches a request against cached ones by similarity; nil leaves
	// the cache exact-only.
	Semantic *SemanticIndex
}

// cacheSettings is the secret-free projection of the `responseCache` setting.
type cacheSettings struct {
	Enabled  bool             `json:"enabled"`
	TTL      time.Duration    `json:"ttl"`
	MaxKB    int              `json:"maxKb"`
	Semantic semanticSettings `json:"semantic"`
}

// semanticSettings is the similarity layer under an enabled response cache. It
// is on unless the setting says otherwise; without an embedding model the cache
// stays exact-only.
type semanticSettings struct {
	Enabled   bool    `json:"enabled"`
	Model     string  `json:"embeddingModel"`
	Threshold float64 `json:"threshold"`
}

const (
	defaultThreshold = 0.92
	minThreshold     = 0.80
	maxThreshold     = 0.99
)

// projectSettings reads the setting, filling what is missing or out of range
// with the defaults. Anything that is not an object is off.
func projectSettings(raw string, present bool) cacheSettings {
	s := cacheSettings{TTL: defaultCacheTTL, MaxKB: defaultCacheEntryKB,
		Semantic: semanticSettings{Enabled: true, Threshold: defaultThreshold}}
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
	if sem := v.Get("semantic"); sem.IsObject() {
		if e := sem.Get("enabled"); e.Type == gjson.True || e.Type == gjson.False {
			s.Semantic.Enabled = e.Type == gjson.True
		}
		if m := sem.Get("embeddingModel"); m.Type == gjson.String && len(m.String()) <= 256 {
			s.Semantic.Model = strings.TrimSpace(m.String())
		}
		if t := sem.Get("threshold"); t.Type == gjson.Number && t.Float() >= minThreshold && t.Float() <= maxThreshold {
			s.Semantic.Threshold = t.Float()
		}
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
	// An injected plan is not in the key, which hashes the client's own body, so
	// a planned request neither reads nor stores an entry.
	if c.planInjected || c.in.Cache == cacheBypass || c.clientStream || c.in.Caller.Anonymous || c.in.Caller.ID == "" {
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
		return c.cachedSimilar(ctx, s)
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

// cachedSimilar answers a request whose exact key is missing but whose text is
// close to one already cached. It leaves the vector for remember, so the answer
// is indexed without a second embedding.
func (c *call) cachedSimilar(ctx context.Context, s cacheSettings) (Output, bool) {
	// A combo member before this one may have left its vector; it indexes that
	// member's answer, not this one's.
	c.cacheVec, c.cacheBucket = nil, ""
	rc := c.p.d.ResponseCache
	if rc == nil || rc.Semantic == nil || c.p.d.Embed == nil || !s.Semantic.Enabled || s.Semantic.Model == "" {
		return Output{}, false
	}
	if len(c.req.Messages) > 3 || len(c.req.Tools) > 0 {
		return Output{}, false
	}
	text := c.req.LastUserText()
	if text == "" {
		return Output{}, false
	}
	vec, err := c.p.d.Embed(ctx, s.Semantic.Model, text)
	if err != nil || len(vec) == 0 {
		return Output{}, false
	}
	bucket := c.semanticBucket(ctx)
	c.cacheVec, c.cacheBucket = vec, bucket
	key, score, ok := rc.Semantic.Lookup(bucket, vec, s.Semantic.Threshold)
	if !ok {
		return Output{}, false
	}
	raw, hit := rc.Cache.Get(cache.Response, key)
	if !hit {
		rc.Semantic.Drop(key)
		return Output{}, false
	}
	var e cachedAnswer
	if json.Unmarshal(raw, &e) != nil || len(e.Body) == 0 {
		rc.Semantic.Drop(key)
		return Output{}, false
	}
	r := &c.row
	r.Cached = true
	r.PromptTokens, r.CompletionTokens, r.CachedTokens, r.ReasoningTokens = e.Prompt, e.Completion, e.Cached, e.Reasoning
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set(CacheHeader, cacheHit)
	h.Set(CacheSimilarity, strconv.FormatFloat(score, 'f', 3, 64))
	return Output{Status: http.StatusOK, Header: h, Body: e.Body}, true
}

// semanticBucket is everything that must match before similarity decides: the
// request's identity and settings, and its body with the last user text
// blanked, so only that text is compared.
func (c *call) semanticBucket(ctx context.Context) string {
	body := c.canonicalWithLastUserBlanked()
	t := c.tctx.Thinking
	return cacheKeyBody([]string{
		"semantic", c.in.Caller.ID, c.format, c.dest.provider, c.dest.model, c.client,
		t.Mode, t.Level, strconv.Itoa(t.Budget), t.Display, c.saverKey(ctx), c.skillKey(ctx),
	}, body)
}

// canonicalWithLastUserBlanked rewrites the canonical body's last user content
// to empty; an unreadable body falls back to the body itself.
func (c *call) canonicalWithLastUserBlanked() []byte {
	body, ok := canonicalBody(c.in.Body)
	if !ok {
		return nil
	}
	messages := gjson.GetBytes(body, "messages").Array()
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Get("role").String() != "user" {
			continue
		}
		if next, err := sjson.SetBytes(body, "messages."+strconv.Itoa(i)+".content", ""); err == nil {
			return next
		}
		break
	}
	return body
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
	if rc.Semantic != nil && len(c.cacheVec) > 0 && c.cacheBucket != "" {
		rc.Semantic.Add(c.cacheBucket, c.cacheKey, c.cacheVec)
	}
}
