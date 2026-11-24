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
	apphttp "rllmx/app/http"
	"rllmx/app/store/repository"
)

// KeysController answers the API-key management routes. A key is a secret, so
// every route here needs a dashboard session, and only the create and the reveal
// answer a full value.
type KeysController struct {
	DB    func() *repository.Store
	Quota *quota.Limits
	// Budget reads what a key spent against its budget. Nil leaves the meters out.
	Budget  *quota.Quota
	Codec   identity.Keys
	Machine identity.Machine
	Changed func()
	Log     log.Log
	// Users holds the standard-user accounts and their key assignments. Nil
	// leaves every caller the single operator.
	Users *auth.Users
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
	Quota *quota.Limit `json:"quota,omitempty"`
	// Spend is what the key used in each budget window; absent when it has no
	// budget and when the read failed.
	Spend      map[string]spendView `json:"spend,omitempty"`
	Unreadable bool                 `json:"unreadable,omitempty"`
	MachineID  string               `json:"machineId,omitempty"`
	// ResponseCache is this key's switch for the answer cache.
	ResponseCache bool `json:"responseCache"`
	// Owner is the standard user the key belongs to; absent when nobody's.
	Owner string `json:"owner,omitempty"`
}

// accessView is the allow-list shape the dashboard sends and reads.
type accessView struct {
	Restricted bool     `json:"restricted"`
	Allow      []string `json:"allow"`
}

// spendView is one budget window's use, for the dashboard's meter.
type spendView struct {
	Requests int64   `json:"requests"`
	Tokens   int64   `json:"tokens"`
	Cost     float64 `json:"cost"`
}

// masked is the view of one stored key for a list or an update, with its budget
// when it has one, its answer-cache switch and its owner.
func masked(k repository.APIKey, budget *quota.Limit, cacheOn bool, owner string) keyView {
	allow := k.AccessAllow
	if allow == nil {
		allow = []string{}
	}
	return keyView{
		ID: k.ID, Name: k.Name, Key: auth.MaskKey(k.Key), IsActive: k.IsActive,
		CreatedAt: k.CreatedAt, Unreadable: k.Unreadable, Quota: budget, ResponseCache: cacheOn, Owner: owner,
		Access: accessView{Restricted: k.AccessRestricted, Allow: allow},
	}
}

// revealed is the view of one key with its full value, for the reveal only.
func revealed(k repository.APIKey, budget *quota.Limit, cacheOn bool, owner string) keyView {
	v := masked(k, budget, cacheOn, owner)
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

// owned is the set of keys a standard user may touch. A nil set means every key,
// which is the admin and every caller the guard did not mark. A user with no
// user list owns nothing, so every key reads as someone else's.
func (c *KeysController) owned(ctx contractshttp.Context) (map[string]bool, contractshttp.Response, bool) {
	who, ok := apphttp.SessionFrom(ctx)
	var session *auth.Session
	if ok {
		session = &who
	}
	var lister apphttp.KeyLister
	if c.Users != nil {
		lister = c.Users
	}
	keys, scoped, err := apphttp.ScopedKeys(ctx.Request().Origin().Context(), session, lister)
	if err != nil {
		return nil, storageDown(ctx), false
	}
	if !scoped {
		return nil, nil, true
	}
	set := make(map[string]bool, len(keys))
	for _, id := range keys {
		set[id] = true
	}
	return set, nil, true
}

// ownerMap is every key assignment. A read failure is an error, not an empty
// map: the dashboard sends the owner back on a save, so an empty map would
// unassign a key it merely failed to read.
func (c *KeysController) ownerMap(rc context.Context) (map[string]string, error) {
	if c.Users == nil {
		return nil, nil
	}
	return c.Users.Owners(rc)
}

// fillSpend adds each budgeted key's use. A failed read leaves that key's meter
// out rather than failing the list; the budget itself is still enforced.
func (c *KeysController) fillSpend(rc context.Context, keys []keyView) {
	if c.Budget == nil {
		return
	}
	for i := range keys {
		if keys[i].Quota == nil {
			continue
		}
		used, err := c.Budget.Used(rc, keys[i].ID)
		if err != nil {
			if c.Log != nil {
				c.Log.Warningf("keys: reading a budget's use failed: id=%s", keys[i].ID)
			}
			continue
		}
		spend := make(map[string]spendView, len(used))
		for window, t := range used {
			spend[window] = spendView{Requests: t.Requests, Tokens: t.Tokens, Cost: t.Cost}
		}
		keys[i].Spend = spend
	}
}

// List answers GET /api/keys: every key, oldest first, masked.
func (c *KeysController) List(ctx contractshttp.Context) contractshttp.Response {
	st := c.store()
	if st == nil {
		return storageDown(ctx)
	}
	rc := ctx.Request().Origin().Context()
	set, out3, ok := c.owned(ctx)
	if !ok {
		return out3
	}
	rows, err := st.APIKeys().List(rc)
	if err != nil {
		return storageDown(ctx)
	}
	all, out2, ok := c.limits(ctx)
	if !ok {
		return out2
	}
	cached, err := st.KV().ResponseCacheKeys(rc)
	if err != nil {
		// A switch that cannot be read is off, as a single key's is.
		cached = nil
	}
	owners, err := c.ownerMap(rc)
	if err != nil {
		return storageDown(ctx)
	}
	out := make([]keyView, 0, len(rows))
	for _, k := range rows {
		if set != nil && !set[k.ID] {
			continue
		}
		out = append(out, masked(k, budgetOf(all, k.ID), cached[k.ID], owners[k.ID]))
	}
	c.fillSpend(rc, out)
	return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{"keys": out})
}

// Show answers GET /api/keys/{id}: one key, masked like the list. A GET is a
// plain navigation the browser sends with its cookie, so it never carries the
// value; Reveal does.
func (c *KeysController) Show(ctx contractshttp.Context) contractshttp.Response {
	return c.view(ctx, false)
}

// Reveal answers POST /api/keys/{id}/reveal: the one answer besides create that
// carries a key's full value. It is a write-class call, so the same-origin gate
// covers it, and it is logged with the key's id, never the value.
func (c *KeysController) Reveal(ctx contractshttp.Context) contractshttp.Response {
	ctx.Response().Header("Cache-Control", "no-store")
	return c.view(ctx, true)
}

func (c *KeysController) view(ctx contractshttp.Context, full bool) contractshttp.Response {
	set, out3, ok := c.owned(ctx)
	if !ok {
		return out3
	}
	k, out, ok := c.one(ctx)
	if !ok {
		return out
	}
	if set != nil && !set[k.ID] {
		return flat(ctx, nethttp.StatusNotFound, "Key not found")
	}
	rc := ctx.Request().Origin().Context()
	if full {
		if c.Log != nil {
			c.Log.Infof("keys: a key was revealed: id=%s key=%s", k.ID, auth.MaskKey(k.Key))
		}
		if k.Unreadable {
			return flat(ctx, nethttp.StatusInternalServerError, "This key's stored value cannot be read")
		}
	}
	all, out2, ok := c.limits(ctx)
	if !ok {
		return out2
	}
	owners, err := c.ownerMap(rc)
	if err != nil {
		return storageDown(ctx)
	}
	build := masked
	if full {
		build = revealed
	}
	one := []keyView{build(k, budgetOf(all, k.ID), cacheOn(c.store(), rc, k.ID), owners[k.ID])}
	c.fillSpend(rc, one)
	return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{"key": one[0]})
}

// Create answers POST /api/keys: a fresh key for this machine, answered once.
func (c *KeysController) Create(ctx contractshttp.Context) contractshttp.Response {
	st := c.store()
	if st == nil {
		return storageDown(ctx)
	}
	var b struct {
		Name  string `json:"name"`
		Owner string `json:"owner"`
	}
	if err := bindJSON(ctx, &b); err != nil {
		return flat(ctx, nethttp.StatusBadRequest, "Invalid request body")
	}
	name := strings.TrimSpace(b.Name)
	if !auth.ValidName(name) {
		return flat(ctx, nethttp.StatusBadRequest, "The name must be 1 to 64 bytes with no control character")
	}
	owner := strings.TrimSpace(b.Owner)
	rc := ctx.Request().Origin().Context()
	if owner != "" {
		if c.Users == nil {
			return flat(ctx, nethttp.StatusBadRequest, "That owner does not exist")
		}
		if _, ok := c.Users.Get(rc, owner); !ok {
			return flat(ctx, nethttp.StatusBadRequest, "That owner does not exist")
		}
	}
	machineID, found := c.Machine.Load(c.Dir)
	if !found {
		return flat(ctx, nethttp.StatusServiceUnavailable, "The machine id is not available yet")
	}
	key, err := c.Codec.New(machineID)
	if err != nil {
		return flat(ctx, nethttp.StatusInternalServerError, "The key could not be generated")
	}
	row, err := st.APIKeys().Create(rc, name, machineID, key)
	if err != nil {
		return flat(ctx, nethttp.StatusInternalServerError, "The key could not be stored")
	}
	if owner != "" {
		if err := c.Users.SetOwner(rc, row.ID, owner); err != nil {
			// A key nobody can see is a key nobody can revoke.
			_, _ = st.APIKeys().Delete(rc, row.ID)
			return ownerRefused(ctx, err)
		}
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
		Owner         *string         `json:"owner"`
	}
	if err := bindJSON(ctx, &b); err != nil {
		return flat(ctx, nethttp.StatusBadRequest, "Invalid request body")
	}
	set, out3, ok := c.owned(ctx)
	if !ok {
		return out3
	}
	if set != nil {
		if !set[id] {
			return flat(ctx, nethttp.StatusNotFound, "Key not found")
		}
		// A standard user renames their key and nothing else: pausing it, its
		// budget, allow-list, cache switch and owner belong to the admin.
		if b.IsActive != nil || len(b.Access) > 0 || len(b.Quota) > 0 || b.ResponseCache != nil || b.Owner != nil || b.Name == nil {
			return flat(ctx, nethttp.StatusForbidden, "A standard user may only rename a key")
		}
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
	// The owner is checked with the other fields, so a refusal leaves the key as
	// it was.
	if b.Owner != nil {
		if c.Users == nil {
			return storageDown(ctx)
		}
		if owner := strings.TrimSpace(*b.Owner); owner != "" {
			if _, ok := c.Users.Get(ctx.Request().Origin().Context(), owner); !ok {
				return flat(ctx, nethttp.StatusBadRequest, "That owner does not exist")
			}
		}
	}
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
	if b.Owner != nil {
		if err := c.Users.SetOwner(rc, id, strings.TrimSpace(*b.Owner)); err != nil {
			return ownerRefused(ctx, err)
		}
	}
	c.changed()
	all, out2, ok := c.limits(ctx)
	if !ok {
		return out2
	}
	owners, err := c.ownerMap(rc)
	if err != nil {
		return storageDown(ctx)
	}
	return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{"key": masked(row, budgetOf(all, row.ID), cacheOn(st, rc, row.ID), owners[row.ID])})
}

// ownerRefused turns an assignment the user list refuses into the answer the
// dashboard shows: a missing user is a bad request, anything else the store.
func ownerRefused(ctx contractshttp.Context, err error) contractshttp.Response {
	if errors.Is(err, auth.ErrUserMissing) || errors.Is(err, auth.ErrUserName) || errors.Is(err, auth.ErrUserEmail) {
		return flat(ctx, nethttp.StatusBadRequest, "That owner does not exist")
	}
	return storageDown(ctx)
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
	// The guard already keeps a standard user off this route; the handler holds
	// the same line so a routing mistake cannot hand them a key's deletion.
	if who, ok := apphttp.SessionFrom(ctx); ok && who.Role == auth.RoleUser {
		return flat(ctx, nethttp.StatusForbidden, "Only the administrator can delete a key")
	}
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
	rc := ctx.Request().Origin().Context()
	if c.Users != nil {
		if err := c.Users.SetOwner(rc, ctx.Request().Route("id"), ""); err != nil && c.Log != nil {
			c.Log.Warningf("keys: the owner of a deleted key could not be cleared: id=%s: %v", ctx.Request().Route("id"), err)
		}
	}
	if c.Quota != nil {
		// A budget with no key is harmless; the row goes with it.
		_ = c.Quota.Delete(rc, ctx.Request().Route("id"))
	}
	_ = st.KV().Delete(rc, repository.ScopeResponseCache, ctx.Request().Route("id"))
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
