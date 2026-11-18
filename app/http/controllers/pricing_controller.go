package controllers

import (
	"context"
	"encoding/json"
	"errors"
	nethttp "net/http"
	"sort"
	"strings"

	contractshttp "github.com/goravel/framework/contracts/http"
	"github.com/goravel/framework/contracts/log"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

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

func (c *PricingController) store() *repository.Store { return storeOf(c.DB) }

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

// errPricingRow says a stored row is not a JSON object of cards.
var errPricingRow = errors.New("pricing: the stored row is not an object")

// mergeCards replaces each named model in the provider's stored row and keeps
// the rest as stored, including their spelling. A row that is not an object is
// an error, so a write never replaces what it cannot read.
func mergeCards(rc context.Context, st *repository.Store, provider string, cards map[string]upstream.Rates) error {
	row := "{}"
	raw, ok, err := st.KV().Get(rc, repository.ScopePricing, provider)
	if err != nil {
		return err
	}
	if ok {
		if !gjson.Parse(raw).IsObject() {
			return errPricingRow
		}
		row = raw
	}
	models := make([]string, 0, len(cards))
	for model := range cards {
		models = append(models, model)
	}
	sort.Strings(models)
	for _, model := range models {
		body, err := json.Marshal(cards[model])
		if err != nil {
			return err
		}
		if row, err = sjson.SetRaw(row, text.EscapePath(model), string(body)); err != nil {
			return err
		}
	}
	return st.KV().Set(rc, repository.ScopePricing, provider, row)
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
		if err := st.KV().DeleteScope(rc, repository.ScopePricing); err != nil {
			return flat(ctx, nethttp.StatusInternalServerError, "Failed to reset pricing")
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
	row := gjson.Parse(raw)
	if !row.IsObject() {
		return errPricingRow
	}
	path := text.EscapePath(model)
	if !row.Get(path).Exists() {
		return nil
	}
	next, err := sjson.Delete(raw, path)
	if err != nil {
		return err
	}
	if len(gjson.Parse(next).Map()) == 0 {
		return st.KV().Delete(rc, repository.ScopePricing, provider)
	}
	return st.KV().Set(rc, repository.ScopePricing, provider, next)
}

// priceName applies the name rule a provider and a model each get.
func priceName(name string, limit int) bool {
	return name != "" && len(name) <= limit && !text.HasControl(name)
}
