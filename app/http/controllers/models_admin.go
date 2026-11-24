package controllers

import (
	"context"
	"encoding/json"
	nethttp "net/http"
	"sort"
	"strings"
	"time"

	contractshttp "github.com/goravel/framework/contracts/http"

	"rllmx/app/gateway/catalog"
	"rllmx/app/llm/routing"
	"rllmx/app/llm/upstream"
	"rllmx/app/platform/text"
	"rllmx/app/store/dialect"
	"rllmx/app/store/document"
	"rllmx/app/store/repository"
)

const maxDisabledIDs = 2000

// modelAdminCaps is the capability part the model page reads.
type modelAdminCaps struct {
	Vision        bool `json:"vision"`
	Audio         bool `json:"audio"`
	Search        bool `json:"search"`
	Reasoning     bool `json:"reasoning"`
	ContextWindow int  `json:"contextWindow"`
	MaxOutput     int  `json:"maxOutput"`
}

// modelAdminView is one model as the dashboard lists it.
type modelAdminView struct {
	Provider    string         `json:"provider"`
	Model       string         `json:"model"`
	Name        string         `json:"name"`
	FullModel   string         `json:"fullModel"`
	RoutedModel string         `json:"routedModel"`
	Alias       string         `json:"alias"`
	Caps        modelAdminCaps `json:"caps"`
	Source      string         `json:"source"`
	Kind        string         `json:"kind,omitempty"`
}

func (m *ModelsController) store() *repository.Store { return storeOf(m.DB) }

func (m *ModelsController) changed() {
	if m.Changed != nil {
		m.Changed()
	}
}

// AdminList answers GET /api/models: every registry model with its capabilities
// and the alias that resolves to it.
func (m *ModelsController) AdminList(ctx contractshttp.Context) contractshttp.Response {
	st := m.store()
	if st == nil || m.Registry == nil {
		return storageDown(ctx)
	}
	rc := ctx.Request().Origin().Context()
	aliases, err := st.KV().List(rc, repository.ScopeModelAliases)
	if err != nil {
		return storageDown(ctx)
	}
	m.refreshDeclared(rc)
	entries := m.Registry.All()
	lists, err := catalog.RegistryAll(rc, st.KV(), entries)
	if err != nil {
		return storageDown(ctx)
	}
	byTarget := aliasTargets(m.Registry, aliases)
	out := make([]modelAdminView, 0, len(entries))
	for i, e := range entries {
		owner := e.Alias
		if owner == "" {
			owner = e.ID
		}
		for _, mm := range lists[i] {
			full := owner + "/" + mm.ID
			v := modelAdminView{
				Provider: owner, Model: mm.ID, Name: mm.Name, FullModel: full,
				RoutedModel: full, Alias: mm.ID, Source: mm.Source, Kind: mm.Kind,
			}
			if a, ok := byTarget[e.ID+"/"+mm.ID]; ok {
				v.Alias = a
			}
			if m.Caps != nil {
				c := m.Caps.For(e.ID, mm.ID)
				v.Caps = modelAdminCaps{Vision: c.Vision, Audio: c.AudioInput, Search: c.Search, Reasoning: c.Reasoning, ContextWindow: c.ContextWindow, MaxOutput: c.MaxOutput}
			}
			out = append(out, v)
		}
	}
	return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{"models": out})
}

// aliasTargets maps "<provider id>/<model>" to the first alias naming it, so the
// list answers in the direction the resolver reads.
func aliasTargets(reg *upstream.Registry, aliases map[string]string) map[string]string {
	names := make([]string, 0, len(aliases))
	for k := range aliases {
		names = append(names, k)
	}
	sort.Strings(names)
	out := make(map[string]string, len(names))
	for _, a := range names {
		p, model, ok := routing.ParseAlias(aliases[a])
		if !ok {
			continue
		}
		id := p
		if resolved, ok := reg.Resolve(p); ok {
			id = resolved
		}
		if _, seen := out[id+"/"+model]; !seen {
			out[id+"/"+model] = a
		}
	}
	return out
}

// Aliases answers GET /api/models/alias: the stored map, alias to target.
func (m *ModelsController) Aliases(ctx contractshttp.Context) contractshttp.Response {
	st := m.store()
	if st == nil {
		return storageDown(ctx)
	}
	raw, err := st.KV().List(ctx.Request().Origin().Context(), repository.ScopeModelAliases)
	if err != nil {
		return storageDown(ctx)
	}
	out := make(map[string]string, len(raw))
	for alias, v := range raw {
		if p, model, ok := routing.ParseAlias(v); ok {
			out[alias] = p + "/" + model
		}
	}
	return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{"aliases": out})
}

// SetAlias answers PUT /api/models/alias. The map is stored alias to target,
// the one direction the resolver reads.
func (m *ModelsController) SetAlias(ctx contractshttp.Context) contractshttp.Response {
	st := m.store()
	if st == nil {
		return storageDown(ctx)
	}
	var b struct {
		Model string `json:"model"`
		Alias string `json:"alias"`
	}
	if err := bindJSON(ctx, &b); err != nil {
		return flat(ctx, nethttp.StatusBadRequest, "Invalid request body")
	}
	model, alias := strings.TrimSpace(b.Model), strings.TrimSpace(b.Alias)
	if model == "" || alias == "" {
		return flat(ctx, nethttp.StatusBadRequest, "Model and alias required")
	}
	if len(model) > comboModelBytes || text.HasControl(model) || !strings.Contains(model, "/") {
		return flat(ctx, nethttp.StatusBadRequest, "The model must be provider/model")
	}
	if len(alias) > comboNameBytes || text.HasControl(alias) || strings.Contains(alias, "/") {
		return flat(ctx, nethttp.StatusBadRequest, "Invalid alias")
	}
	rc := ctx.Request().Origin().Context()
	if msg, err := m.aliasBlocked(rc, st, alias); err != nil {
		return storageDown(ctx)
	} else if msg != "" {
		return flat(ctx, nethttp.StatusBadRequest, msg)
	}
	existing, ok, err := st.KV().Get(rc, repository.ScopeModelAliases, alias)
	if err != nil {
		return storageDown(ctx)
	}
	if ok {
		if p, mm, parsed := routing.ParseAlias(existing); !parsed || p+"/"+mm != model {
			return flat(ctx, nethttp.StatusConflict, "The alias already names another model")
		}
	}
	if err := st.KV().Set(rc, repository.ScopeModelAliases, alias, document.Encode(model)); err != nil {
		return flat(ctx, nethttp.StatusInternalServerError, "Failed to update alias")
	}
	m.changed()
	return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{"success": true, "model": model, "alias": alias})
}

// DeleteAlias answers DELETE /api/models/alias?alias=.
func (m *ModelsController) DeleteAlias(ctx contractshttp.Context) contractshttp.Response {
	st := m.store()
	if st == nil {
		return storageDown(ctx)
	}
	alias := strings.TrimSpace(ctx.Request().Query("alias"))
	if alias == "" {
		return flat(ctx, nethttp.StatusBadRequest, "Alias required")
	}
	if err := st.KV().Delete(ctx.Request().Origin().Context(), repository.ScopeModelAliases, alias); err != nil {
		return flat(ctx, nethttp.StatusInternalServerError, "Failed to delete alias")
	}
	m.changed()
	return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{"success": true})
}

// aliasBlocked refuses an alias that would shadow a name routing already uses.
func (m *ModelsController) aliasBlocked(rc context.Context, st *repository.Store, alias string) (string, error) {
	if m.Registry != nil && m.Registry.Reserved(alias) {
		return "The alias is a provider name", nil
	}
	combos, err := st.Combos().List(rc)
	if err != nil {
		return "", err
	}
	for _, c := range combos {
		if c.Name == alias {
			return "The alias is a combo name", nil
		}
	}
	nodes, err := st.Nodes().List(rc, "")
	if err != nil {
		return "", err
	}
	for _, n := range nodes {
		if p, _ := n.Doc().GetString("prefix"); p != "" && p == alias {
			return "The alias is a node prefix", nil
		}
	}
	return "", nil
}

// CustomModels answers GET /api/models/custom with the stored values.
func (m *ModelsController) CustomModels(ctx contractshttp.Context) contractshttp.Response {
	st := m.store()
	if st == nil {
		return storageDown(ctx)
	}
	raw, err := st.KV().List(ctx.Request().Origin().Context(), repository.ScopeCustomModels)
	if err != nil {
		return storageDown(ctx)
	}
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]json.RawMessage, 0, len(keys))
	for _, k := range keys {
		out = append(out, json.RawMessage(raw[k]))
	}
	return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{"models": out})
}

// customModel is the stored shape of a custom model, the one the catalog and the
// reference both read.
type customModel struct {
	ProviderAlias string          `json:"providerAlias"`
	ID            string          `json:"id"`
	Type          string          `json:"type"`
	Name          string          `json:"name"`
	Caps          map[string]bool `json:"caps,omitempty"`
	Transport     string          `json:"transport,omitempty"`
}

func customKey(owner, id, kind string) string { return owner + "|" + id + "|" + kind }

// refreshDeclared reloads what the operator declared about custom models, so a
// vision or reasoning custom model sorts and adapts as one. A read failure
// leaves the previous set alone.
func (m *ModelsController) refreshDeclared(rc context.Context) {
	if m.Caps == nil || m.DB == nil {
		return
	}
	st := m.store()
	if st == nil {
		return
	}
	raw, err := st.KV().List(rc, repository.ScopeCustomModels)
	if err != nil {
		return
	}
	all := make(map[string]upstream.Capability, len(raw))
	for _, body := range raw {
		var stored customModel
		if json.Unmarshal([]byte(body), &stored) != nil || stored.ProviderAlias == "" || stored.ID == "" {
			continue
		}
		all[stored.ProviderAlias+"/"+stored.ID] = upstream.Capability{
			Vision: stored.Caps["vision"], Reasoning: stored.Caps["reasoning"],
		}
	}
	m.Caps.SetCustom(all)
}

// AddCustomModel answers POST /api/models/custom: one upsert.
func (m *ModelsController) AddCustomModel(ctx contractshttp.Context) contractshttp.Response {
	st := m.store()
	if st == nil {
		return storageDown(ctx)
	}
	var b struct {
		ProviderAlias string          `json:"providerAlias"`
		ID            string          `json:"id"`
		Type          string          `json:"type"`
		Name          string          `json:"name"`
		Caps          map[string]bool `json:"caps"`
		Transport     string          `json:"transport"`
	}
	if err := bindJSON(ctx, &b); err != nil {
		return flat(ctx, nethttp.StatusBadRequest, "Invalid request body")
	}
	owner, id, kind, msg := m.customTarget(ctx, st, b.ProviderAlias, b.ID, b.Type)
	if msg != "" {
		return flat(ctx, nethttp.StatusBadRequest, msg)
	}
	rc := ctx.Request().Origin().Context()
	key := customKey(owner, id, kind)
	stored := customModel{}
	existing, ok, err := st.KV().Get(rc, repository.ScopeCustomModels, key)
	if err != nil {
		return storageDown(ctx)
	}
	if ok {
		_ = json.Unmarshal([]byte(existing), &stored)
	}
	stored.ProviderAlias, stored.ID, stored.Type = owner, id, kind
	if name := strings.TrimSpace(b.Name); name != "" {
		if len(name) > comboModelBytes || text.HasControl(name) {
			return flat(ctx, nethttp.StatusBadRequest, "Invalid custom model name")
		}
		stored.Name = name
	} else if stored.Name == "" {
		stored.Name = id
	}
	if len(b.Caps) > 0 {
		stored.Caps = map[string]bool{}
		for _, k := range []string{"vision", "reasoning"} {
			if v, ok := b.Caps[k]; ok {
				stored.Caps[k] = v
			}
		}
	}
	if kind == upstream.KindSTT && strings.TrimSpace(b.Transport) == "gemini-live" {
		stored.Transport = "gemini-live"
	} else if kind != upstream.KindSTT {
		stored.Transport = ""
	}
	body, err := json.Marshal(stored)
	if err != nil {
		return flat(ctx, nethttp.StatusInternalServerError, "Failed to add custom model")
	}
	if err := st.KV().Set(rc, repository.ScopeCustomModels, key, string(body)); err != nil {
		return flat(ctx, nethttp.StatusInternalServerError, "Failed to add custom model")
	}
	m.refreshDeclared(rc)
	return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{"success": true, "added": !ok})
}

// DeleteCustomModel answers DELETE /api/models/custom?providerAlias&id&type.
func (m *ModelsController) DeleteCustomModel(ctx contractshttp.Context) contractshttp.Response {
	st := m.store()
	if st == nil {
		return storageDown(ctx)
	}
	q := ctx.Request()
	owner, id, kind, msg := m.customTarget(ctx, st, q.Query("providerAlias"), q.Query("id"), q.Query("type"))
	if msg != "" {
		return flat(ctx, nethttp.StatusBadRequest, msg)
	}
	rc := ctx.Request().Origin().Context()
	if err := st.KV().Delete(rc, repository.ScopeCustomModels, customKey(owner, id, kind)); err != nil {
		return flat(ctx, nethttp.StatusInternalServerError, "Failed to delete custom model")
	}
	m.refreshDeclared(rc)
	return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{"success": true})
}

// customTarget validates a custom model's owner, id and kind.
func (m *ModelsController) customTarget(ctx contractshttp.Context, st *repository.Store, provider, id, kind string) (string, string, string, string) {
	provider, id = strings.TrimSpace(provider), strings.TrimSpace(id)
	if provider == "" || id == "" {
		return "", "", "", "providerAlias and id required"
	}
	if len(id) > comboModelBytes || text.HasControl(id) {
		return "", "", "", "Invalid custom model id"
	}
	if !m.knownProvider(ctx.Request().Origin().Context(), st, provider) {
		return "", "", "", "Unknown provider"
	}
	kind = strings.TrimSpace(kind)
	if kind == "" {
		kind = upstream.KindLLM
	}
	if !upstream.KnownKind(kind) {
		return "", "", "", "Unknown model type"
	}
	return provider, id, kind, ""
}

// knownProvider reports whether a name is a registry provider or a node.
func (m *ModelsController) knownProvider(rc context.Context, st *repository.Store, name string) bool {
	if m.Registry != nil && m.Registry.Reserved(name) {
		return true
	}
	nodes, err := st.Nodes().List(rc, "")
	if err != nil {
		return false
	}
	for _, n := range nodes {
		if p, _ := n.Doc().GetString("prefix"); n.ID == name || (p != "" && p == name) {
			return true
		}
	}
	return false
}

// DisabledModels answers GET /api/models/disabled, one owner or every owner.
func (m *ModelsController) DisabledModels(ctx contractshttp.Context) contractshttp.Response {
	st := m.store()
	if st == nil {
		return storageDown(ctx)
	}
	rc := ctx.Request().Origin().Context()
	if name := strings.TrimSpace(ctx.Request().Query("providerAlias")); name != "" {
		list, err := st.KV().DisabledModels(rc, name)
		if err != nil {
			return storageDown(ctx)
		}
		return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{"ids": list})
	}
	raw, err := st.KV().List(rc, repository.ScopeDisabledModels)
	if err != nil {
		return storageDown(ctx)
	}
	out := make(map[string][]string, len(raw))
	for k, v := range raw {
		out[k] = repository.StoredStrings(v)
	}
	return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{"disabled": out})
}

// DisableModels answers POST /api/models/disabled: the request's ids are added
// to the owner's list as a set union.
func (m *ModelsController) DisableModels(ctx contractshttp.Context) contractshttp.Response {
	st := m.store()
	if st == nil {
		return storageDown(ctx)
	}
	var b struct {
		ProviderAlias string   `json:"providerAlias"`
		IDs           []string `json:"ids"`
	}
	if err := bindJSON(ctx, &b); err != nil {
		return flat(ctx, nethttp.StatusBadRequest, "Invalid request body")
	}
	owner := strings.TrimSpace(b.ProviderAlias)
	if owner == "" || b.IDs == nil || len(owner) > comboNameBytes || text.HasControl(owner) {
		return flat(ctx, nethttp.StatusBadRequest, "providerAlias and ids[] required")
	}
	rc := ctx.Request().Origin().Context()
	current, err := st.KV().DisabledModels(rc, owner)
	if err != nil {
		return storageDown(ctx)
	}
	seen := make(map[string]bool, len(current)+len(b.IDs))
	list := make([]string, 0, len(current)+len(b.IDs))
	for _, id := range append(current, b.IDs...) {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		if len(id) > comboModelBytes || text.HasControl(id) {
			return flat(ctx, nethttp.StatusBadRequest, "Invalid model id")
		}
		seen[id] = true
		list = append(list, id)
	}
	if len(list) > maxDisabledIDs {
		return flat(ctx, nethttp.StatusBadRequest, "Too many disabled models for one provider")
	}
	if err := st.KV().Set(rc, repository.ScopeDisabledModels, owner, repository.StoredStringsValue(list)); err != nil {
		return flat(ctx, nethttp.StatusInternalServerError, "Failed to disable models")
	}
	return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{"success": true})
}

// EnableModels answers DELETE /api/models/disabled?providerAlias[&id].
func (m *ModelsController) EnableModels(ctx contractshttp.Context) contractshttp.Response {
	st := m.store()
	if st == nil {
		return storageDown(ctx)
	}
	q := ctx.Request()
	owner := strings.TrimSpace(q.Query("providerAlias"))
	if owner == "" {
		return flat(ctx, nethttp.StatusBadRequest, "providerAlias required")
	}
	rc := ctx.Request().Origin().Context()
	id := strings.TrimSpace(q.Query("id"))
	if id == "" {
		if err := st.KV().Delete(rc, repository.ScopeDisabledModels, owner); err != nil {
			return flat(ctx, nethttp.StatusInternalServerError, "Failed to enable models")
		}
		return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{"success": true})
	}
	current, err := st.KV().DisabledModels(rc, owner)
	if err != nil {
		return storageDown(ctx)
	}
	list := make([]string, 0, len(current))
	for _, v := range current {
		if v != id {
			list = append(list, v)
		}
	}
	if len(list) == len(current) {
		return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{"success": true})
	}
	if len(list) == 0 {
		err = st.KV().Delete(rc, repository.ScopeDisabledModels, owner)
	} else {
		err = st.KV().Set(rc, repository.ScopeDisabledModels, owner, repository.StoredStringsValue(list))
	}
	if err != nil {
		return flat(ctx, nethttp.StatusInternalServerError, "Failed to enable models")
	}
	return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{"success": true})
}

// availabilityView is one lock or one unavailable account, as the badge reads it.
type availabilityView struct {
	Provider       string  `json:"provider"`
	Model          string  `json:"model"`
	Status         string  `json:"status"`
	Until          string  `json:"until,omitempty"`
	ConnectionID   string  `json:"connectionId"`
	ConnectionName string  `json:"connectionName"`
	LastError      *string `json:"lastError"`
}

// Availability answers GET /api/models/availability: the locks that still hold,
// and the accounts marked unavailable that hold none.
func (m *ModelsController) Availability(ctx contractshttp.Context) contractshttp.Response {
	st := m.store()
	if st == nil {
		return storageDown(ctx)
	}
	rows, err := st.Connections().List(ctx.Request().Origin().Context(), repository.ConnFilter{})
	if err != nil {
		return storageDown(ctx)
	}
	now := time.Now()
	out := make([]availabilityView, 0, len(rows))
	for _, c := range rows {
		if c.Corrupt {
			continue
		}
		d := document.From(c.Data)
		var last *string
		if v, ok := d.GetString("lastError"); ok && v != "" {
			last = &v
		}
		locked := false
		for _, k := range d.Keys() {
			if !strings.HasPrefix(k, repository.LockPrefix) {
				continue
			}
			v, ok := d.GetString(k)
			if !ok {
				continue
			}
			until, err := dialect.ParseTimestamp(v)
			if err != nil || !until.After(now) {
				continue
			}
			locked = true
			model := strings.TrimPrefix(k, repository.LockPrefix)
			if model == "" {
				model = "__all"
			}
			out = append(out, availabilityView{
				Provider: c.Provider, Model: model, Status: "cooldown", Until: v,
				ConnectionID: c.ID, ConnectionName: connectionName(c), LastError: last,
			})
		}
		if status, _ := d.GetString("testStatus"); !locked && status == "unavailable" {
			out = append(out, availabilityView{
				Provider: c.Provider, Model: "__all", Status: "unavailable",
				ConnectionID: c.ID, ConnectionName: connectionName(c), LastError: last,
			})
		}
	}
	return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{"models": out, "unavailableCount": len(out)})
}

func connectionName(c repository.Connection) string {
	switch {
	case c.Name != "":
		return c.Name
	case c.Email != "":
		return c.Email
	}
	return c.ID
}

// ClearCooldown answers POST /api/models/availability: it drops the lock a row
// names. "__all" is the account lock, addressed by its real key.
func (m *ModelsController) ClearCooldown(ctx contractshttp.Context) contractshttp.Response {
	st := m.store()
	if st == nil {
		return storageDown(ctx)
	}
	var b struct {
		Action   string `json:"action"`
		Provider string `json:"provider"`
		Model    string `json:"model"`
	}
	if err := bindJSON(ctx, &b); err != nil {
		return flat(ctx, nethttp.StatusBadRequest, "Invalid request")
	}
	provider, model := strings.TrimSpace(b.Provider), strings.TrimSpace(b.Model)
	if b.Action != "clearCooldown" || provider == "" || model == "" {
		return flat(ctx, nethttp.StatusBadRequest, "Invalid request")
	}
	if m.Registry != nil {
		if id, ok := m.Registry.Resolve(provider); ok {
			provider = id
		}
	}
	rc := ctx.Request().Origin().Context()
	rows, err := st.Connections().List(rc, repository.ConnFilter{Provider: provider})
	if err != nil {
		return storageDown(ctx)
	}
	now := time.Now()
	for _, c := range rows {
		if _, ok := document.From(c.Data).GetString(repository.LockKey(model)); !ok {
			continue
		}
		if err := st.Connections().ClearLocks(rc, c.ID, model, now, time.Time{}); err != nil {
			return flat(ctx, nethttp.StatusInternalServerError, "Failed to clear cooldown")
		}
	}
	return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{"ok": true})
}
