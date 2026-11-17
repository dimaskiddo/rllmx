package controllers

import (
	"context"
	"errors"
	nethttp "net/http"
	"sort"
	"strings"

	contractshttp "github.com/goravel/framework/contracts/http"

	"rllmx/app/llm/routing"
	"rllmx/app/llm/upstream"
	"rllmx/app/platform/text"
	"rllmx/app/store/repository"
)

// comboStrategiesKey holds the per-combo strategy a rename must carry and a
// delete must drop.
const comboStrategiesKey = "comboStrategies"

// List answers GET /api/combos.
func (c *CombosController) List(ctx contractshttp.Context) contractshttp.Response {
	st := c.store()
	if st == nil {
		return storageDown(ctx)
	}
	rows, err := st.Combos().List(ctx.Request().Origin().Context())
	if err != nil {
		return storageDown(ctx)
	}
	out := make([]contractshttp.Json, 0, len(rows))
	for _, row := range rows {
		out = append(out, comboJSON(row))
	}
	return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{"combos": out})
}

// Show answers GET /api/combos/{id}.
func (c *CombosController) Show(ctx contractshttp.Context) contractshttp.Response {
	st := c.store()
	if st == nil {
		return storageDown(ctx)
	}
	row, ok, err := st.Combos().GetByID(ctx.Request().Origin().Context(), ctx.Request().Route("id"))
	if err != nil {
		return storageDown(ctx)
	}
	if !ok {
		return flat(ctx, nethttp.StatusNotFound, "Combo not found")
	}
	return ctx.Response().Json(nethttp.StatusOK, comboJSON(row))
}

// comboBody is a create request. Models may be empty: a media combo is filled
// after it is created.
type comboBody struct {
	Name   string   `json:"name"`
	Models []string `json:"models"`
	Kind   string   `json:"kind"`
}

// Create answers POST /api/combos.
func (c *CombosController) Create(ctx contractshttp.Context) contractshttp.Response {
	st := c.store()
	if st == nil {
		return storageDown(ctx)
	}
	var b comboBody
	if err := bindJSON(ctx, &b); err != nil {
		return flat(ctx, nethttp.StatusBadRequest, "Invalid request body")
	}
	name, models, msg := validCombo(acceptBody{Name: b.Name, Models: b.Models}, 0)
	if msg != "" {
		return flat(ctx, nethttp.StatusBadRequest, msg)
	}
	kind, msg := comboKind(b.Kind)
	if msg != "" {
		return flat(ctx, nethttp.StatusBadRequest, msg)
	}
	created, err := st.Combos().Create(ctx.Request().Origin().Context(), name, kind, models)
	if errors.Is(err, repository.ErrComboNameTaken) {
		return flat(ctx, nethttp.StatusConflict, "A combo with that name exists")
	}
	if err != nil {
		return flat(ctx, nethttp.StatusInternalServerError, "Failed to create combo")
	}
	c.changed(created.Name)
	return ctx.Response().Json(nethttp.StatusCreated, comboJSON(created))
}

// Update answers PUT /api/combos/{id}. A rename carries the combo's strategy
// with it, which the reference leaves behind under the old name.
func (c *CombosController) Update(ctx contractshttp.Context) contractshttp.Response {
	st := c.store()
	if st == nil {
		return storageDown(ctx)
	}
	rc := ctx.Request().Origin().Context()
	id := ctx.Request().Route("id")
	prev, ok, err := st.Combos().GetByID(rc, id)
	if err != nil {
		return storageDown(ctx)
	}
	if !ok {
		return flat(ctx, nethttp.StatusNotFound, "Combo not found")
	}
	var b struct {
		Name   *string   `json:"name"`
		Kind   *string   `json:"kind"`
		Models *[]string `json:"models"`
	}
	if err := bindJSON(ctx, &b); err != nil {
		return flat(ctx, nethttp.StatusBadRequest, "Invalid request body")
	}
	patch := repository.ComboPatch{Kind: b.Kind, Models: b.Models}
	if b.Name != nil {
		name, msg := comboName(*b.Name)
		if msg != "" {
			return flat(ctx, nethttp.StatusBadRequest, msg)
		}
		patch.Name = &name
	}
	if b.Kind != nil {
		kind, msg := comboKind(*b.Kind)
		if msg != "" {
			return flat(ctx, nethttp.StatusBadRequest, msg)
		}
		patch.Kind = &kind
	}
	if b.Models != nil {
		models, msg := comboModels(*b.Models, 0)
		if msg != "" {
			return flat(ctx, nethttp.StatusBadRequest, msg)
		}
		patch.Models = &models
	}
	updated, err := st.Combos().Update(rc, id, patch)
	switch {
	case errors.Is(err, repository.ErrComboNameTaken):
		return flat(ctx, nethttp.StatusConflict, "A combo with that name exists")
	case errors.Is(err, repository.ErrNotFound):
		return flat(ctx, nethttp.StatusNotFound, "Combo not found")
	case err != nil:
		return flat(ctx, nethttp.StatusInternalServerError, "Failed to update combo")
	}
	if updated.Name != prev.Name {
		if err := st.Settings().RenameObjectField(rc, comboStrategiesKey, prev.Name, updated.Name); err != nil {
			if c.Log != nil {
				c.Log.Warningf("combos: strategy of %q did not move to %q: %v", prev.Name, updated.Name, err)
			}
			return flat(ctx, nethttp.StatusInternalServerError, "The combo was renamed but its strategy did not move")
		}
	}
	c.changed(prev.Name, updated.Name)
	return ctx.Response().Json(nethttp.StatusOK, comboJSON(updated))
}

// Delete answers DELETE /api/combos/{id}, dropping the combo's strategy too.
func (c *CombosController) Delete(ctx contractshttp.Context) contractshttp.Response {
	st := c.store()
	if st == nil {
		return storageDown(ctx)
	}
	rc := ctx.Request().Origin().Context()
	prev, ok, err := st.Combos().GetByID(rc, ctx.Request().Route("id"))
	if err != nil {
		return storageDown(ctx)
	}
	if !ok {
		return flat(ctx, nethttp.StatusNotFound, "Combo not found")
	}
	deleted, err := st.Combos().Delete(rc, prev.ID)
	if err != nil {
		return flat(ctx, nethttp.StatusInternalServerError, "Failed to delete combo")
	}
	if !deleted {
		return flat(ctx, nethttp.StatusNotFound, "Combo not found")
	}
	if err := st.Settings().MergeObjectField(rc, comboStrategiesKey, prev.Name, "null"); err != nil && c.Log != nil {
		c.Log.Warningf("combos: strategy of %q was left behind: %v", prev.Name, err)
	}
	c.changed(prev.Name)
	return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{"success": true})
}

// presetItem is one combo a preset list would create.
type presetItem struct {
	Name   string   `json:"name"`
	Models []string `json:"models"`
	Exists bool     `json:"exists"`
}

// Presets answers GET /api/combos/presets?source=.
func (c *CombosController) Presets(ctx contractshttp.Context) contractshttp.Response {
	st := c.store()
	if st == nil {
		return storageDown(ctx)
	}
	rc := ctx.Request().Origin().Context()
	source := strings.TrimSpace(ctx.Request().Query("source"))
	if source != "claude" && source != "cursor" {
		return flat(ctx, nethttp.StatusBadRequest, "source must be 'cursor' or 'claude'")
	}
	items, err := c.presetItems(rc, st, source)
	if err != nil {
		return storageDown(ctx)
	}
	toCreate := 0
	for _, it := range items {
		if !it.Exists {
			toCreate++
		}
	}
	return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{
		"source": source, "items": items, "toCreate": toCreate, "toSkip": len(items) - toCreate,
	})
}

// ApplyPresets answers POST /api/combos/presets: the missing presets are
// created, and an existing name is never overwritten.
func (c *CombosController) ApplyPresets(ctx contractshttp.Context) contractshttp.Response {
	st := c.store()
	if st == nil {
		return storageDown(ctx)
	}
	var b struct {
		Source string `json:"source"`
	}
	if err := bindJSON(ctx, &b); err != nil {
		return flat(ctx, nethttp.StatusBadRequest, "Invalid request body")
	}
	source := strings.TrimSpace(b.Source)
	if source != "claude" && source != "cursor" {
		return flat(ctx, nethttp.StatusBadRequest, "source must be 'cursor' or 'claude'")
	}
	rc := ctx.Request().Origin().Context()
	items, err := c.presetItems(rc, st, source)
	if err != nil {
		return storageDown(ctx)
	}
	created := make([]contractshttp.Json, 0, len(items))
	skipped := make([]string, 0, len(items))
	for _, it := range items {
		if it.Exists {
			skipped = append(skipped, it.Name)
			continue
		}
		row, err := st.Combos().Create(rc, it.Name, "", it.Models)
		if errors.Is(err, repository.ErrComboNameTaken) {
			skipped = append(skipped, it.Name)
			continue
		}
		if err != nil {
			return flat(ctx, nethttp.StatusInternalServerError, "Failed to create combo presets")
		}
		created = append(created, comboJSON(row))
	}
	if len(created) > 0 {
		c.changed()
	}
	return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{
		"source": source, "created": created, "skipped": skipped,
		"createdCount": len(created), "skippedCount": len(skipped),
	})
}

// presetItems builds a source's list, de-duplicated by name, with the names that
// already exist marked. A name or member list a combo could not hold is dropped.
func (c *CombosController) presetItems(rc context.Context, st *repository.Store, source string) ([]presetItem, error) {
	var raw []presetItem
	switch source {
	case "claude":
		raw = c.registryPresets("claude")
		aliases, err := st.KV().List(rc, repository.ScopeModelAliases)
		if err != nil {
			return nil, err
		}
		raw = append(raw, c.aliasPresets(aliases, "claude")...)
	case "cursor":
		raw = c.registryPresets("cursor")
	}
	rows, err := st.Combos().List(rc)
	if err != nil {
		return nil, err
	}
	taken := make(map[string]bool, len(rows))
	for _, r := range rows {
		taken[r.Name] = true
	}
	seen := make(map[string]bool, len(raw))
	out := make([]presetItem, 0, len(raw))
	for _, it := range raw {
		name, msg := comboName(it.Name)
		if msg != "" || seen[name] {
			continue
		}
		models, msg := comboModels(it.Models, 1)
		if msg != "" {
			continue
		}
		seen[name] = true
		out = append(out, presetItem{Name: name, Models: models, Exists: taken[name]})
	}
	return out, nil
}

// registryPresets is every chat model of one provider, named by its id.
func (c *CombosController) registryPresets(id string) []presetItem {
	if c.Registry == nil {
		return nil
	}
	e, ok := c.Registry.ByID(id)
	if !ok {
		return nil
	}
	owner := e.Alias
	if owner == "" {
		owner = e.ID
	}
	out := make([]presetItem, 0, len(e.Models))
	for _, m := range e.Models {
		if m.Kind != "" && m.Kind != upstream.KindLLM {
			continue
		}
		out = append(out, presetItem{Name: m.ID, Models: []string{owner + "/" + m.ID}})
	}
	return out
}

// aliasPresets names the stored aliases that point at one provider.
func (c *CombosController) aliasPresets(aliases map[string]string, id string) []presetItem {
	if c.Registry == nil {
		return nil
	}
	names := make([]string, 0, len(aliases))
	for k := range aliases {
		names = append(names, k)
	}
	sort.Strings(names)
	out := make([]presetItem, 0, len(names))
	for _, alias := range names {
		p, _, ok := routing.ParseAlias(aliases[alias])
		if !ok {
			continue
		}
		resolved := p
		if r, ok := c.Registry.Resolve(p); ok {
			resolved = r
		}
		if resolved != id || text.HasControl(alias) {
			continue
		}
		out = append(out, presetItem{Name: alias, Models: []string{alias}})
	}
	return out
}
