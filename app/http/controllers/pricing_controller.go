package controllers

import (
	"context"
	"encoding/json"
	nethttp "net/http"
	"sort"
	"strings"

	contractshttp "github.com/goravel/framework/contracts/http"
	"github.com/goravel/framework/contracts/log"
	"github.com/tidwall/gjson"

	"rllmx/app/llm/upstream"
	"rllmx/app/platform/text"
	"rllmx/app/store/repository"
)

// priceFields are the card fields a write accepts, the stored spellings.
var priceFields = map[string]bool{
	"input": true, "output": true, "cached": true, "reasoning": true,
	"cache_creation": true, "cacheCreation": true,
}

// PricingController answers the pricing routes. The table is process-wide, so
// the controller reaches it through the source rather than holding it.
type PricingController struct {
	DB func() *repository.Store
	// Table returns the merged table the pipeline prices with.
	Table func() (*upstream.Table, error)
	// Reload drops the built table and warms it again, so the next request
	// prices with the new cards.
	Reload func()
	Log    log.Log
}

func (c *PricingController) store() *repository.Store {
	if c.DB == nil {
		return nil
	}
	return c.DB()
}

// cards is the merged view the routes answer with.
func (c *PricingController) cards(ctx contractshttp.Context) contractshttp.Response {
	t, err := c.Table()
	if err != nil || t == nil {
		return storageDown(ctx)
	}
	return ctx.Response().Json(nethttp.StatusOK, t.Cards())
}

// Show answers GET /api/pricing.
func (c *PricingController) Show(ctx contractshttp.Context) contractshttp.Response {
	return c.cards(ctx)
}

// Update answers PATCH /api/pricing: the request's cards are merged into the
// stored rows, one model at a time.
func (c *PricingController) Update(ctx contractshttp.Context) contractshttp.Response {
	st := c.store()
	if st == nil || c.Table == nil || c.Reload == nil {
		return storageDown(ctx)
	}
	var body map[string]json.RawMessage
	if err := bindJSON(ctx, &body); err != nil || len(body) == 0 {
		return flat(ctx, nethttp.StatusBadRequest, "Invalid pricing data format")
	}
	providers := make([]string, 0, len(body))
	for provider := range body {
		providers = append(providers, provider)
	}
	sort.Strings(providers)
	updates := make(map[string]map[string]upstream.Rates, len(body))
	for _, provider := range providers {
		if !priceName(provider, comboNameBytes) {
			return flat(ctx, nethttp.StatusBadRequest, "Invalid pricing for provider: "+provider)
		}
		var models map[string]json.RawMessage
		if err := json.Unmarshal(body[provider], &models); err != nil || models == nil {
			return flat(ctx, nethttp.StatusBadRequest, "Invalid pricing for provider: "+provider)
		}
		cards := make(map[string]upstream.Rates, len(models))
		for model, card := range models {
			if !priceName(model, comboModelBytes) {
				return flat(ctx, nethttp.StatusBadRequest, "Invalid pricing for model: "+provider+"/"+model)
			}
			if msg := checkCard(provider, model, card); msg != "" {
				return flat(ctx, nethttp.StatusBadRequest, msg)
			}
			r, err := upstream.ParseCard(card)
			if err != nil {
				return flat(ctx, nethttp.StatusBadRequest, "Invalid pricing for model: "+provider+"/"+model)
			}
			cards[model] = r
		}
		updates[provider] = cards
	}
	rc := ctx.Request().Origin().Context()
	for _, provider := range providers {
		if err := mergeCards(rc, st, provider, updates[provider]); err != nil {
			return flat(ctx, nethttp.StatusInternalServerError, "Failed to update pricing")
		}
	}
	c.Reload()
	return c.cards(ctx)
}

// checkCard refuses an unknown field or a negative value by name, the words the
// pricing form shows.
func checkCard(provider, model string, card json.RawMessage) string {
	obj := gjson.ParseBytes(card)
	if !obj.IsObject() {
		return "Invalid pricing for model: " + provider + "/" + model
	}
	var bad string
	obj.ForEach(func(k, v gjson.Result) bool {
		if !priceFields[k.String()] {
			bad = "Invalid pricing field: " + k.String() + " for " + provider + "/" + model
			return false
		}
		if v.Type != gjson.Number || v.Num < 0 {
			bad = "Invalid pricing value for " + k.String() + " in " + provider + "/" + model + ": must be non-negative number"
			return false
		}
		return true
	})
	return bad
}

// mergeCards replaces each named model in the provider's stored row and keeps
// the rest as stored, including their spelling.
func mergeCards(rc context.Context, st *repository.Store, provider string, cards map[string]upstream.Rates) error {
	row := map[string]json.RawMessage{}
	raw, ok, err := st.KV().Get(rc, repository.ScopePricing, provider)
	if err != nil {
		return err
	}
	if ok {
		_ = json.Unmarshal([]byte(raw), &row)
	}
	for model, r := range cards {
		body, err := json.Marshal(r)
		if err != nil {
			return err
		}
		row[model] = body
	}
	body, err := json.Marshal(row)
	if err != nil {
		return err
	}
	return st.KV().Set(rc, repository.ScopePricing, provider, string(body))
}

// Reset answers DELETE /api/pricing?provider[&model]: the named rows go, or the
// whole scope when the request names nothing.
func (c *PricingController) Reset(ctx contractshttp.Context) contractshttp.Response {
	st := c.store()
	if st == nil || c.Reload == nil {
		return storageDown(ctx)
	}
	rc := ctx.Request().Origin().Context()
	provider := strings.TrimSpace(ctx.Request().Query("provider"))
	model := strings.TrimSpace(ctx.Request().Query("model"))
	switch {
	case provider == "":
		raw, err := st.KV().List(rc, repository.ScopePricing)
		if err != nil {
			return flat(ctx, nethttp.StatusInternalServerError, "Failed to reset pricing")
		}
		for p := range raw {
			if err := st.KV().Delete(rc, repository.ScopePricing, p); err != nil {
				return flat(ctx, nethttp.StatusInternalServerError, "Failed to reset pricing")
			}
		}
	case model == "":
		if err := st.KV().Delete(rc, repository.ScopePricing, provider); err != nil {
			return flat(ctx, nethttp.StatusInternalServerError, "Failed to reset pricing")
		}
	default:
		if err := dropCard(rc, st, provider, model); err != nil {
			return flat(ctx, nethttp.StatusInternalServerError, "Failed to reset pricing")
		}
	}
	c.Reload()
	return c.cards(ctx)
}

func dropCard(rc context.Context, st *repository.Store, provider, model string) error {
	raw, ok, err := st.KV().Get(rc, repository.ScopePricing, provider)
	if err != nil || !ok {
		return err
	}
	row := map[string]json.RawMessage{}
	if err := json.Unmarshal([]byte(raw), &row); err != nil {
		return err
	}
	if _, found := row[model]; !found {
		return nil
	}
	delete(row, model)
	if len(row) == 0 {
		return st.KV().Delete(rc, repository.ScopePricing, provider)
	}
	body, err := json.Marshal(row)
	if err != nil {
		return err
	}
	return st.KV().Set(rc, repository.ScopePricing, provider, string(body))
}

// priceName applies the name rule a provider and a model each get.
func priceName(name string, limit int) bool {
	return name != "" && len(name) <= limit && !text.HasControl(name)
}
