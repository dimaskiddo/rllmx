package controllers

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	contractshttp "github.com/goravel/framework/contracts/http"
)

// suggestedModel is one model a provider's own list names.
type suggestedModel struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	ContextLength int    `json:"contextLength,omitempty"`
}

const (
	// suggestedTTL keeps a provider's list for ten minutes; kilo's changes
	// slowly enough for an hour.
	suggestedTTL = 10 * time.Minute
	kiloTTL      = time.Hour
	kiloModels   = "https://api.kilo.ai/api/gateway/models"
)

// suggestedSources is the fixed address and filter of every provider the
// dashboard may ask about. A caller names a provider, never a URL, so this
// route cannot be used to read another host.
var suggestedSources = map[string]struct{ url, filter string }{
	"openrouter":          {"https://openrouter.ai/api/v1/models", "openrouter-free"},
	"kilocode":            {"https://api.kilo.ai/api/gateway/models", "openrouter-free"},
	"kilo-gateway":        {"https://api.kilo.ai/api/gateway/models", "openrouter-free"},
	"opencode":            {"https://opencode.ai/zen/v1/models", "opencode-free"},
	"opencode-zen":        {"https://opencode.ai/zen/v1/models", "opencode-free"},
	"opencode-go":         {"https://opencode.ai/inference/go/v1/models", "opencode-go"},
	"api-airforce":        {"https://api.airforce/v1/models", "airforce-free"},
	"minimax-code":        {"https://agent.minimax.io/mavis/api/v1/models?region=en&buildEnv=prod", "minimax-code"},
	"minimax-code-global": {"https://agent.minimax.io/mavis/api/v1/models?region=en&buildEnv=prod", "minimax-code"},
}

// SuggestedModels answers GET /api/providers/suggested-models?provider= with the
// provider's own list, filtered to the free models it serves.
func (c *ProvidersController) SuggestedModels(ctx contractshttp.Context) contractshttp.Response {
	provider := strings.TrimSpace(ctx.Request().Query("provider"))
	source, ok := suggestedSources[provider]
	if !ok || c.Fetch == nil {
		return flat(ctx, http.StatusBadRequest, "Unknown provider")
	}
	out, err := c.suggested(ctx.Request().Origin().Context(), provider, source.url, source.filter, suggestedTTL)
	if err != nil {
		return ctx.Response().Json(http.StatusOK, contractshttp.Json{"data": []suggestedModel{}})
	}
	return ctx.Response().Json(http.StatusOK, contractshttp.Json{"data": out})
}

// KiloFreeModels answers GET /api/providers/kilo/free-models with the gateway's
// free models, kept for an hour; a failed refresh answers what was kept.
func (c *ProvidersController) KiloFreeModels(ctx contractshttp.Context) contractshttp.Response {
	if c.Fetch == nil {
		return flat(ctx, http.StatusServiceUnavailable, "The model list is not available")
	}
	c.suggestMu.Lock()
	kept, keptAt := c.kiloBody, c.kiloAt
	c.suggestMu.Unlock()
	if len(kept) > 0 && time.Since(keptAt) < kiloTTL {
		return ctx.Response().Json(http.StatusOK, contractshttp.Json{"models": kept, "cached": true})
	}
	body, err := c.Fetch(ctx.Request().Origin().Context(), kiloModels)
	if err != nil {
		stale := kept
		if len(stale) > 0 {
			return ctx.Response().Json(http.StatusOK, contractshttp.Json{"models": stale, "cached": true, "warning": "the list could not be refreshed"})
		}
		return ctx.Response().Json(http.StatusBadGateway, contractshttp.Json{"models": []suggestedModel{}, "error": "the model list could not be read"})
	}
	models := filterModels(body, "airforce-free", true)
	c.suggestMu.Lock()
	c.kiloBody, c.kiloAt = models, time.Now()
	c.suggestMu.Unlock()
	return ctx.Response().Json(http.StatusOK, contractshttp.Json{"models": models, "cached": false})
}

// suggested answers one provider's list from the cache or by reading it.
func (c *ProvidersController) suggested(ctx context.Context, provider, address, filter string, ttl time.Duration) ([]suggestedModel, error) {
	c.suggestMu.Lock()
	if at, ok := c.suggestAt[provider]; ok && time.Since(at) < ttl {
		body := c.suggestBody[provider]
		c.suggestMu.Unlock()
		return body, nil
	}
	c.suggestMu.Unlock()
	raw, err := c.Fetch(ctx, address)
	if err != nil {
		return nil, err
	}
	out := filterModels(raw, filter, false)
	c.suggestMu.Lock()
	if c.suggestAt == nil {
		c.suggestAt = map[string]time.Time{}
		c.suggestBody = map[string][]suggestedModel{}
	}
	c.suggestAt[provider], c.suggestBody[provider] = time.Now(), out
	c.suggestMu.Unlock()
	return out, nil
}

// filterModels applies one filter to a provider's answer. Kilo's list names its
// free models directly; every other filter derives the answer.
func filterModels(raw []byte, filter string, kilo bool) []suggestedModel {
	data := gjson.ParseBytes(raw)
	items := data.Get("data")
	if !items.IsArray() {
		items = data.Get("models")
	}
	out := make([]suggestedModel, 0, len(items.Array()))
	if filter == "minimax-code" {
		for _, provider := range data.Get("providers").Array() {
			if provider.Get("id").String() != "minimax" {
				continue
			}
			provider.Get("config.models").ForEach(func(key, value gjson.Result) bool {
				out = append(out, suggestedModel{ID: key.String(), Name: value.Get("name").String(), ContextLength: int(value.Get("context_length").Int())})
				return true
			})
		}
		return out
	}
	for _, item := range items.Array() {
		id := item.Get("id").String()
		name := item.Get("name").String()
		if name == "" {
			name = id
		}
		if id == "" {
			continue
		}
		m := suggestedModel{ID: id, Name: name, ContextLength: int(item.Get("context_length").Int())}
		switch filter {
		case "openrouter-free":
			free := item.Get("pricing.prompt").String() == "0" && item.Get("pricing.completion").String() == "0"
			if !free && item.Get("pricing").Exists() {
				continue
			}
			if m.ContextLength < 200000 {
				continue
			}
		case "opencode-free":
			if !strings.HasSuffix(id, "-free") && id != "big-pickle" || id == "deepseek-v4-flash-free" {
				continue
			}
		case "opencode-go":
		case "airforce-free":
			// Kilo's own answer marks its free rows.
			if kilo {
				if !item.Get("isFree").Bool() {
					continue
				}
				break
			}
			tier := strings.ToLower(item.Get("tier").String())
			if tier != "free" && !strings.HasSuffix(id, ":free") {
				continue
			}
		default:
			continue
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
