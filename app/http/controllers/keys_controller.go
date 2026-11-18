package controllers

import (
	"context"
	"encoding/json"
	"errors"
	nethttp "net/http"
	"strings"

	contractshttp "github.com/goravel/framework/contracts/http"
	"github.com/goravel/framework/contracts/log"
	"github.com/tidwall/gjson"

	"rllmx/app/access/auth"
	"rllmx/app/access/identity"
	"rllmx/app/access/quota"
	"rllmx/app/store/repository"
)

// KeysController answers the API-key management routes. A key is a secret, so
// every route here needs a dashboard session, and only the create and the reveal
// answer a full value.
type KeysController struct {
	DB      func() *repository.Store
	Quota   *quota.Limits
	Codec   identity.Keys
	Machine identity.Machine
	Changed func()
	Log     log.Log
	// Dir is DATA_DIR, where the machine id lives. The server creates it at
	// start-up, so this only reads it.
	Dir string
}

// keyView is one key as the list answers it: masked, never the value.
type keyView struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Key       string     `json:"key"`
	IsActive  bool       `json:"isActive"`
	CreatedAt string     `json:"createdAt"`
	Access    accessView `json:"access"`
	// Quota is the key's budget; absent when it has none.
	Quota      *quota.Limit `json:"quota,omitempty"`
	Unreadable bool         `json:"unreadable,omitempty"`
	MachineID  string       `json:"machineId,omitempty"`
	// ResponseCache is this key's switch for the answer cache.
	ResponseCache bool `json:"responseCache"`
}

// accessView is the allow-list shape the dashboard sends and reads.
type accessView struct {
	Restricted bool     `json:"restricted"`
	Allow      []string `json:"allow"`
}

// masked is the view of one stored key for a list or an update, with its budget
// when it has one and its answer-cache switch.
func masked(k repository.APIKey, budget *quota.Limit, cacheOn bool) keyView {
	allow := k.AccessAllow
	if allow == nil {
		allow = []string{}
	}
	return keyView{
		ID: k.ID, Name: k.Name, Key: auth.MaskKey(k.Key), IsActive: k.IsActive,
		CreatedAt: k.CreatedAt, Unreadable: k.Unreadable, Quota: budget, ResponseCache: cacheOn,
		Access: accessView{Restricted: k.AccessRestricted, Allow: allow},
	}
}

// revealed is the view of one key with its full value, for the reveal only.
func revealed(k repository.APIKey, budget *quota.Limit, cacheOn bool) keyView {
	v := masked(k, budget, cacheOn)
	if !k.Unreadable {
		v.Key = k.Key
	}
	return v
}

// cacheOn reports whether this key may use the answer cache. A read failure is
// off, the same as no row.
func cacheOn(st *repository.Store, ctx context.Context, id string) bool {
	on, err := st.KV().ResponseCacheEnabled(ctx, id)
	return err == nil && on
}

// limits returns every key's budget, or a failure the dashboard answers as a
// storage fault.
func (c *KeysController) limits(ctx contractshttp.Context) (map[string]quota.Limit, contractshttp.Response, bool) {
	if c.Quota == nil {
		return nil, storageDown(ctx), false
	}
	all, err := c.Quota.All(ctx.Request().Origin().Context())
	if err != nil {
		return nil, storageDown(ctx), false
	}
	return all, nil, true
}

// budgetOf is one key's budget, or nil when it has none.
func budgetOf(all map[string]quota.Limit, id string) *quota.Limit {
	if l, ok := all[id]; ok {
		return &l
	}
	return nil
}

func (c *KeysController) store() *repository.Store { return storeOf(c.DB) }

// List answers GET /api/keys: every key, oldest first, masked.
func (c *KeysController) List(ctx contractshttp.Context) contractshttp.Response {
	st := c.store()
	if st == nil {
		return storageDown(ctx)
	}
	rows, err := st.APIKeys().List(ctx.Request().Origin().Context())
	if err != nil {
		return storageDown(ctx)
	}
	all, out2, ok := c.limits(ctx)
	if !ok {
		return out2
	}
	cached, err := st.KV().ResponseCacheKeys(ctx.Request().Origin().Context())
	if err != nil {
		// A switch that cannot be read is off, as a single key's is.
		cached = nil
	}
	out := make([]keyView, 0, len(rows))
	for _, k := range rows {
		out = append(out, masked(k, budgetOf(all, k.ID), cached[k.ID]))
	}
	return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{"keys": out})
}

// Show answers GET /api/keys/{id}: one key, and the only answer besides create
// that carries its full value. The reveal is logged with the key's id, never the
// value.
func (c *KeysController) Show(ctx contractshttp.Context) contractshttp.Response {
	k, out, ok := c.one(ctx)
	if !ok {
		return out
	}
	if c.Log != nil {
		c.Log.Infof("keys: a key was revealed: id=%s key=%s", k.ID, auth.MaskKey(k.Key))
	}
	if k.Unreadable {
		return flat(ctx, nethttp.StatusInternalServerError, "This key's stored value cannot be read")
	}
	all, out2, ok := c.limits(ctx)
	if !ok {
		return out2
	}
	return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{"key": revealed(k, budgetOf(all, k.ID), cacheOn(c.store(), ctx.Request().Origin().Context(), k.ID))})
}

// Create answers POST /api/keys: a fresh key for this machine, answered once.
func (c *KeysController) Create(ctx contractshttp.Context) contractshttp.Response {
	st := c.store()
	if st == nil {
		return storageDown(ctx)
	}
	var b struct {
		Name string `json:"name"`
	}
	if err := bindJSON(ctx, &b); err != nil {
		return flat(ctx, nethttp.StatusBadRequest, "Invalid request body")
	}
	name := strings.TrimSpace(b.Name)
	if !auth.ValidName(name) {
		return flat(ctx, nethttp.StatusBadRequest, "The name must be 1 to 64 bytes with no control character")
	}
	machineID, found := c.Machine.Load(c.Dir)
	if !found {
		return flat(ctx, nethttp.StatusServiceUnavailable, "The machine id is not available yet")
	}
	key, err := c.Codec.New(machineID)
	if err != nil {
		return flat(ctx, nethttp.StatusInternalServerError, "The key could not be generated")
	}
	row, err := st.APIKeys().Create(ctx.Request().Origin().Context(), name, machineID, key)
	if err != nil {
		return flat(ctx, nethttp.StatusInternalServerError, "The key could not be stored")
	}
	c.changed()
	return ctx.Response().Json(nethttp.StatusCreated, contractshttp.Json{
		"id": row.ID, "key": key, "name": row.Name, "machineId": machineID,
	})
}

// Update answers PUT /api/keys/{id}: rename, enable or disable, and set the
// allow-list. Every field is optional; an unknown one is refused.
func (c *KeysController) Update(ctx contractshttp.Context) contractshttp.Response {
	st := c.store()
	if st == nil {
		return storageDown(ctx)
	}
	id := ctx.Request().Route("id")
	var b struct {
		Name          *string         `json:"name"`
		IsActive      *bool           `json:"isActive"`
		Access        json.RawMessage `json:"access"`
		Quota         json.RawMessage `json:"quota"`
		ResponseCache *bool           `json:"responseCache"`
	}
	if err := bindJSON(ctx, &b); err != nil {
		return flat(ctx, nethttp.StatusBadRequest, "Invalid request body")
	}
	patch := repository.APIKeyPatch{}
	if b.Name != nil {
		name := strings.TrimSpace(*b.Name)
		if !auth.ValidName(name) {
			return flat(ctx, nethttp.StatusBadRequest, "The name must be 1 to 64 bytes with no control character")
		}
		patch.Name = &name
	}
	patch.IsActive = b.IsActive
	if len(b.Access) > 0 && string(b.Access) != "null" {
		checked, err := auth.ValidateAccess(gjson.ParseBytes(b.Access))
		if err != nil {
			return flat(ctx, nethttp.StatusBadRequest, strings.ToUpper(err.Error()[:1])+err.Error()[1:])
		}
		patch.Access = &checked
	}
	// Every field is checked before any is stored, so a refusal leaves the key as
	// it was.
	var budget *quota.Limit
	if len(b.Quota) > 0 && string(b.Quota) != "null" {
		limit, err := quota.Validate(gjson.ParseBytes(b.Quota))
		if err != nil {
			return flat(ctx, nethttp.StatusBadRequest, strings.ToUpper(err.Error()[:1])+err.Error()[1:])
		}
		if c.Quota == nil {
			return storageDown(ctx)
		}
		budget = &limit
	}
	row, err := st.APIKeys().Update(ctx.Request().Origin().Context(), id, patch)
	if errors.Is(err, repository.ErrNotFound) {
		return flat(ctx, nethttp.StatusNotFound, "Key not found")
	}
	if err != nil {
		return flat(ctx, nethttp.StatusInternalServerError, "The key could not be updated")
	}
	rc := ctx.Request().Origin().Context()
	if budget != nil {
		if err := c.storeBudget(rc, id, *budget); err != nil {
			return flat(ctx, nethttp.StatusInternalServerError, "The key's budget could not be stored")
		}
	}
	if b.ResponseCache != nil {
		if err := setCacheSwitch(rc, st, id, *b.ResponseCache); err != nil {
			return flat(ctx, nethttp.StatusInternalServerError, "The key's cache switch could not be stored")
		}
	}
	c.changed()
	all, out2, ok := c.limits(ctx)
	if !ok {
		return out2
	}
	return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{"key": masked(row, budgetOf(all, row.ID), cacheOn(st, ctx.Request().Origin().Context(), row.ID))})
}

// storeBudget stores a key's budget. An empty one is how a client clears it.
func (c *KeysController) storeBudget(ctx context.Context, id string, budget quota.Limit) error {
	if budget.Empty() {
		return c.Quota.Clear(ctx, id)
	}
	return c.Quota.Set(ctx, id, budget)
}

// setCacheSwitch turns a key's answer cache on or off. The switch is a kv row,
// not a column: a 9router schema has nowhere to put it, and a key without the
// row is off.
func setCacheSwitch(ctx context.Context, st *repository.Store, id string, on bool) error {
	if on {
		return st.KV().Set(ctx, repository.ScopeResponseCache, id, `{"enabled":true}`)
	}
	return st.KV().Delete(ctx, repository.ScopeResponseCache, id)
}

// Delete answers DELETE /api/keys/{id}.
func (c *KeysController) Delete(ctx contractshttp.Context) contractshttp.Response {
	st := c.store()
	if st == nil {
		return storageDown(ctx)
	}
	deleted, err := st.APIKeys().Delete(ctx.Request().Origin().Context(), ctx.Request().Route("id"))
	if err != nil {
		return flat(ctx, nethttp.StatusInternalServerError, "The key could not be deleted")
	}
	if !deleted {
		return flat(ctx, nethttp.StatusNotFound, "Key not found")
	}
	if c.Quota != nil {
		// A budget with no key is harmless; the row goes with it.
		_ = c.Quota.Delete(ctx.Request().Origin().Context(), ctx.Request().Route("id"))
	}
	_ = st.KV().Delete(ctx.Request().Origin().Context(), repository.ScopeResponseCache, ctx.Request().Route("id"))
	c.changed()
	return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{"message": "Key deleted successfully"})
}

// one reads the key a route names.
func (c *KeysController) one(ctx contractshttp.Context) (repository.APIKey, contractshttp.Response, bool) {
	st := c.store()
	if st == nil {
		return repository.APIKey{}, storageDown(ctx), false
	}
	k, found, err := st.APIKeys().GetByID(ctx.Request().Origin().Context(), ctx.Request().Route("id"))
	if err != nil {
		return repository.APIKey{}, storageDown(ctx), false
	}
	if !found {
		return repository.APIKey{}, flat(ctx, nethttp.StatusNotFound, "Key not found"), false
	}
	return k, nil, true
}

// changed drops every cached key so a revocation or an edit takes effect at
// once rather than at the cache's own expiry.
func (c *KeysController) changed() {
	if c.Quota != nil {
		c.Quota.Changed()
	}
	if c.Changed != nil {
		c.Changed()
	}
}
