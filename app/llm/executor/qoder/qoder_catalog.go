package qoder

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"rllmx/app/llm/executor"
	"rllmx/app/llm/upstream"
	"rllmx/app/platform/cache"
	"rllmx/app/platform/file"
	"rllmx/app/protocol/cosy"
	"rllmx/app/store/dialect"
)

// qoderCatalogTTL is how long an account's model list is kept: the gateway
// sends a model's exact configuration back, and a request with the wrong one is
// silently served by another model.
const qoderCatalogTTL = time.Hour

var (
	// ErrNoModelConfig means the account's catalog does not know the model and no
	// static configuration covers it, so nothing was sent.
	ErrNoModelConfig = errors.New("executor: the provider's catalog does not know this model")
)

// qoderStaticConfigs are the blocks the catalog is known to leave out for some
// accounts, so a model it omits still reaches the gateway. A catalog entry
// always wins over one of these.
var qoderStaticConfigs = map[string]string{
	"smodel": `{"model":"","format":"openai","source":"system","url":"","key":"smodel","display_name":"Sonus","is_vl":true,"is_reasoning":true,"max_input_tokens":180000}`,
	"cmodel": `{"model":"","format":"openai","source":"system","url":"","key":"cmodel","display_name":"Cantus","is_vl":true,"is_reasoning":true,"max_input_tokens":180000}`,
}

// qoderCatalog caches one account's model list and its blocks.
type qoderCatalog struct {
	blocks map[string]string
}

// qoderCatalogs fetches and keeps each account's model list. It is held by the
// executor, which is one per process.
func (q *Qoder) catalog(ctx context.Context, req executor.Request) (qoderCatalog, error) {
	if q.Cache == nil {
		return q.fetchCatalog(ctx, req)
	}
	key := "qoder:" + dialect.HashKey(req.Key, q.regionOf(req))
	raw, err := q.Cache.Load(ctx, cache.Connections, key, qoderCatalogTTL, func(ctx context.Context) ([]byte, error) {
		cat, err := q.fetchCatalog(ctx, req)
		if err != nil {
			return nil, err
		}
		return []byte(cat.encode()), nil
	})
	if err != nil {
		return qoderCatalog{}, err
	}
	return decodeCatalog(string(raw)), nil
}

// fetchCatalog signs and sends one model-list request. The list is read and
// kept, not logged: it describes what the account may call.
func (q *Qoder) fetchCatalog(ctx context.Context, req executor.Request) (qoderCatalog, error) {
	if q.Public == nil {
		return qoderCatalog{}, executor.ErrBadAccount
	}
	url := q.host(req) + "/algo/api/v2/model/list"
	h, err := cosy.Headers(q.Public, url, nil, q.identity(req))
	if err != nil {
		return qoderCatalog{}, executor.ErrBadAccount
	}
	h["Accept"] = "application/json"
	h["Accept-Encoding"] = "identity"
	call := executor.Request{URL: url, Method: http.MethodGet, NoRedirect: true, Auth: &upstream.Auth{Headers: h}}
	resp, err := q.Default.Send(ctx, call)
	if err != nil {
		return qoderCatalog{}, err
	}
	defer resp.Body.Close()
	raw := file.ReadUpTo(resp.Body, 4<<20)
	if resp.Status < 200 || resp.Status > 299 {
		return qoderCatalog{}, executor.ErrBadAccount
	}
	cat := qoderCatalog{blocks: map[string]string{}}
	for _, entry := range gjson.GetBytes(raw, "chat").Array() {
		key := entry.Get("key").String()
		if key == "" || len(key) > 128 {
			continue
		}
		// A block is kept even for a model the dashboard hides: the gateway still
		// serves it, and a chat call needs its exact configuration.
		cat.blocks[key] = entry.Raw
	}
	if len(cat.blocks) == 0 {
		return qoderCatalog{}, executor.ErrBadAccount
	}
	return cat, nil
}

// block returns a model's configuration: the account's own, then the static one
// for a model the catalog leaves out.
func (c qoderCatalog) block(model string) (string, bool) {
	if raw, ok := c.blocks[model]; ok {
		return raw, true
	}
	raw, ok := qoderStaticConfigs[model]
	return raw, ok
}

// encode writes the catalog as the one value the cache holds.
func (c qoderCatalog) encode() string {
	out := `{}`
	for k, v := range c.blocks {
		if next, err := sjson.SetRawBytes([]byte(out), k, []byte(v)); err == nil {
			out = string(next)
		}
	}
	return out
}

// decodeCatalog reads what encode wrote.
func decodeCatalog(raw string) qoderCatalog {
	cat := qoderCatalog{blocks: map[string]string{}}
	root := gjson.Parse(raw)
	if !root.IsObject() {
		return cat
	}
	for k, v := range root.Map() {
		cat.blocks[k] = v.Raw
	}
	return cat
}

// qoderKey is the model as the gateway knows it, without a provider prefix.
func qoderKey(model string) string {
	if i := strings.LastIndexByte(model, '/'); i >= 0 {
		return model[i+1:]
	}
	return model
}
