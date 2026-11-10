package controllers

import (
	"context"
	"encoding/json"
	"errors"
	nethttp "net/http"
	"strconv"
	"strings"
	"unicode"

	contractshttp "github.com/goravel/framework/contracts/http"
	"github.com/tidwall/gjson"

	"rllmx/app/llm/upstream"
	"rllmx/app/platform/text"
	"rllmx/app/store/document"
	"rllmx/app/store/repository"
)

const (
	maxBodyBytes   = 1 << 20
	maxPSDBytes    = 16 << 10
	maxPSDDepth    = 3
	maxNameBytes   = 256
	maxSecretBytes = 16 << 10

	maxOverrideHeaders = 20
	maxHeaderValue     = 8192
)

// blockedHeaders are request-structure and credential headers an override may
// never set.
var blockedHeaders = map[string]bool{
	"host": true, "content-length": true, "content-type": true, "connection": true,
	"transfer-encoding": true, "authorization": true, "cookie": true,
}

// ProvidersController answers the provider-connection routes.
type ProvidersController struct {
	Registry *upstream.Registry
	DB       func() *repository.Store
	Checker  *Checker
	// Refresher renews and checks the tokens of OAuth accounts; nil leaves
	// those accounts untestable.
	Refresher LoginChecker
}

// LoginChecker renews an OAuth account's token when it is due and says whether
// the account holds a usable one.
type LoginChecker interface {
	Ensure(ctx context.Context, id string) (refreshed bool, err error)
}

func (c *ProvidersController) store() *repository.Store {
	if c.DB == nil {
		return nil
	}
	return c.DB()
}

// storage answers a failed or missing store without naming the cause.
func storageDown(ctx contractshttp.Context) contractshttp.Response {
	return flat(ctx, nethttp.StatusServiceUnavailable, "Storage is not available")
}

// List answers GET /api/providers.
func (c *ProvidersController) List(ctx contractshttp.Context) contractshttp.Response {
	st := c.store()
	if st == nil {
		return storageDown(ctx)
	}
	rc := ctx.Request().Origin().Context()
	conns, err := st.Connections().List(rc, repository.ConnFilter{})
	if err != nil {
		return storageDown(ctx)
	}
	names := nodeNames(rc, st)
	out := make([]map[string]any, 0, len(conns))
	for _, conn := range conns {
		v := connectionView(conn)
		if upstream.NodeKind(conn.Provider) != "" && conn.Name == "" {
			if n := names[conn.Provider]; n != "" {
				v["name"] = n
			}
		}
		out = append(out, v)
	}
	return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{"connections": out})
}

func nodeNames(ctx context.Context, st *repository.Store) map[string]string {
	nodes, err := st.Nodes().List(ctx, "")
	if err != nil {
		return nil
	}
	out := make(map[string]string, len(nodes))
	for _, n := range nodes {
		out[n.ID] = n.Name
	}
	return out
}

// Show answers GET /api/providers/{id}.
func (c *ProvidersController) Show(ctx contractshttp.Context) contractshttp.Response {
	st := c.store()
	if st == nil {
		return storageDown(ctx)
	}
	conn, ok, err := st.Connections().Get(ctx.Request().Origin().Context(), ctx.Request().Route("id"))
	if err != nil {
		return storageDown(ctx)
	}
	if !ok {
		return flat(ctx, nethttp.StatusNotFound, "Connection not found")
	}
	return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{"connection": connectionView(conn)})
}

type createBody struct {
	ID                   string          `json:"id"`
	Provider             string          `json:"provider"`
	APIKey               string          `json:"apiKey"`
	Name                 string          `json:"name"`
	DisplayName          string          `json:"displayName"`
	GlobalPriority       *int64          `json:"globalPriority"`
	DefaultModel         *string         `json:"defaultModel"`
	AllowOverwrite       bool            `json:"allowOverwrite"`
	Overwrite            bool            `json:"overwrite"`
	ProviderSpecificData json.RawMessage `json:"providerSpecificData"`
}

// Create answers POST /api/providers. Only pasted credentials are created here;
// an OAuth login has its own flow.
func (c *ProvidersController) Create(ctx contractshttp.Context) contractshttp.Response {
	st := c.store()
	if st == nil {
		return storageDown(ctx)
	}
	var b createBody
	if err := bindJSON(ctx, &b); err != nil {
		return flat(ctx, nethttp.StatusBadRequest, "Invalid request body")
	}
	rc := ctx.Request().Origin().Context()

	provider := strings.TrimSpace(b.Provider)
	if id, ok := c.Registry.Resolve(provider); ok {
		provider = id
	}
	cookie := false
	var node *repository.Node
	switch {
	case upstream.NodeKind(provider) != "":
		n, found, err := st.Nodes().Get(rc, provider)
		if err != nil {
			return storageDown(ctx)
		}
		if !found {
			return flat(ctx, nethttp.StatusNotFound, "Provider node not found")
		}
		node = &n
	default:
		e, ok := c.Registry.ByID(provider)
		if !ok {
			return flat(ctx, nethttp.StatusBadRequest, "Invalid provider")
		}
		switch e.Category {
		case upstream.CategoryAPIKey, upstream.CategoryFreeTier:
		case upstream.CategoryCookie:
			cookie = true
		default:
			return flat(ctx, nethttp.StatusBadRequest, "Invalid provider")
		}
	}

	secretName := "API Key"
	if cookie {
		secretName = "Cookie value"
	}
	if strings.TrimSpace(b.APIKey) == "" {
		return flat(ctx, nethttp.StatusBadRequest, secretName+" is required")
	}
	if len(b.APIKey) > maxSecretBytes || hasControl(b.APIKey) {
		return flat(ctx, nethttp.StatusBadRequest, "Invalid "+strings.ToLower(secretName))
	}

	if hasControl(b.Name) || hasControl(b.DisplayName) || hasControl(b.Provider) {
		return flat(ctx, nethttp.StatusBadRequest, "Invalid characters in the request")
	}
	name := strings.TrimSpace(text.FirstNonEmpty(b.Name, b.DisplayName))
	if name == "" {
		if e, ok := c.Registry.ByID(provider); ok {
			name = e.Display.Name
		} else if node != nil {
			name = node.Name
		}
	}
	if name == "" {
		return flat(ctx, nethttp.StatusBadRequest, "Name is required")
	}
	if len(name) > maxNameBytes || hasControl(name) {
		return flat(ctx, nethttp.StatusBadRequest, "Invalid name")
	}

	psd, perr := cleanPSD(b.ProviderSpecificData)
	if perr != "" {
		return flat(ctx, nethttp.StatusBadRequest, perr)
	}
	if node != nil {
		psd = nodeFields(*node)
	}

	fields := map[string]string{"apiKey": document.Encode(b.APIKey), "testStatus": `"unknown"`}
	if v := strings.TrimSpace(b.DisplayName); v != "" && len(v) <= maxNameBytes && !hasControl(v) {
		fields["displayName"] = document.Encode(v)
	}
	if b.GlobalPriority != nil {
		fields["globalPriority"] = strconv.FormatInt(*b.GlobalPriority, 10)
	}
	if b.DefaultModel != nil && len(*b.DefaultModel) <= maxNameBytes && !hasControl(*b.DefaultModel) {
		fields["defaultModel"] = document.Encode(*b.DefaultModel)
	}
	authType := "apikey"
	if cookie {
		authType = "cookie"
	}
	overwrite := b.ID != "" || b.AllowOverwrite || b.Overwrite
	conn, err := st.Connections().Create(rc, repository.ConnInput{
		Provider: provider, AuthType: authType, Name: name, Fields: fields, PSD: psd,
	}, &overwrite)
	if err != nil {
		var conflict *repository.ErrNameConflict
		if errors.As(err, &conflict) {
			return ctx.Response().Json(nethttp.StatusConflict, contractshttp.Json{
				"error": "A connection with this name already exists", "code": "PROVIDER_NAME_CONFLICT",
				"existingId": conflict.ExistingID, "existingName": conflict.ExistingName,
			})
		}
		return flat(ctx, nethttp.StatusInternalServerError, "Failed to create provider")
	}
	return ctx.Response().Json(nethttp.StatusCreated, contractshttp.Json{"connection": connectionView(conn)})
}

// nodeFields are the node values a connection copies, as a JSON object.
func nodeFields(n repository.Node) string {
	d := n.Doc()
	out := map[string]any{"nodeName": n.Name}
	for _, k := range []string{"prefix", "apiType", "baseUrl"} {
		if v, ok := d.GetString(k); ok {
			out[k] = v
		}
	}
	raw, _ := json.Marshal(out)
	return string(raw)
}

// refusedUpdate are the fields only the server writes: credentials, health and
// locks, and the values a node connection copies from its node.
var refusedUpdate = []string{
	"accessToken", "refreshToken", "idToken", "expiresAt", "expiresIn", "tokenType", "scope", "projectId",
	"testStatus", "lastError", "lastErrorAt", "lastTested", "errorCode", "rateLimitedUntil", "backoffLevel",
	"consecutiveUseCount", "lastUsedAt", "lastRefreshAt", "provider", "authType", "email", "id", "createdAt", "updatedAt",
}

var nodeOwned = []string{"prefix", "apiType", "baseUrl", "nodeName"}

// Update answers PUT /api/providers/{id}.
func (c *ProvidersController) Update(ctx contractshttp.Context) contractshttp.Response {
	st := c.store()
	if st == nil {
		return storageDown(ctx)
	}
	id := ctx.Request().Route("id")
	rc := ctx.Request().Origin().Context()
	existing, ok, err := st.Connections().Get(rc, id)
	if err != nil {
		return storageDown(ctx)
	}
	if !ok {
		return flat(ctx, nethttp.StatusNotFound, "Connection not found")
	}
	if existing.Corrupt {
		return flat(ctx, nethttp.StatusConflict, "Connection data is unreadable")
	}

	var raw map[string]json.RawMessage
	if err := bindJSON(ctx, &raw); err != nil {
		return flat(ctx, nethttp.StatusBadRequest, "Invalid request body")
	}
	for _, f := range refusedUpdate {
		if _, set := raw[f]; set {
			return flat(ctx, nethttp.StatusBadRequest, "Field "+f+" cannot be changed here")
		}
	}
	for _, f := range nodeOwned {
		if _, set := raw[f]; set {
			return flat(ctx, nethttp.StatusBadRequest, "Field "+f+" belongs to the provider node")
		}
	}

	patch := repository.ConnPatch{Fields: map[string]string{}}
	var psdSet map[string]string
	var psdRemove []string
	for key, val := range raw {
		switch key {
		case "name":
			var s string
			if json.Unmarshal(val, &s) != nil || strings.TrimSpace(s) == "" || len(s) > maxNameBytes || hasControl(s) {
				return flat(ctx, nethttp.StatusBadRequest, "Invalid name")
			}
			s = strings.TrimSpace(s)
			patch.Name = &s
		case "isActive":
			var v bool
			if json.Unmarshal(val, &v) != nil {
				return flat(ctx, nethttp.StatusBadRequest, "Invalid isActive")
			}
			patch.IsActive = &v
		case "priority":
			var v int64
			if json.Unmarshal(val, &v) != nil || v < 1 || v > 1_000_000 {
				return flat(ctx, nethttp.StatusBadRequest, "Invalid priority")
			}
			patch.Priority = &v
		case "globalPriority":
			var v *int64
			if json.Unmarshal(val, &v) != nil || (v != nil && (*v < 0 || *v > 1_000_000)) {
				return flat(ctx, nethttp.StatusBadRequest, "Invalid globalPriority")
			}
			if v == nil {
				patch.Fields[key] = "null"
			} else {
				patch.Fields[key] = strconv.FormatInt(*v, 10)
			}
		case "defaultModel", "displayName":
			var v *string
			if json.Unmarshal(val, &v) != nil || (v != nil && (len(*v) > maxNameBytes || hasControl(*v))) {
				return flat(ctx, nethttp.StatusBadRequest, "Invalid "+key)
			}
			if v == nil {
				patch.Fields[key] = "null"
			} else {
				patch.Fields[key] = document.Encode(*v)
			}
		case "apiKey":
			var v string
			if json.Unmarshal(val, &v) != nil || len(v) > maxSecretBytes || hasControl(v) {
				return flat(ctx, nethttp.StatusBadRequest, "Invalid api key")
			}
			// An empty key keeps the saved one: the edit form cannot read it back.
			if v != "" && existing.AuthType == "apikey" {
				patch.Fields["apiKey"] = document.Encode(v)
			}
		case "providerSpecificData":
			set, remove, msg := splitPSD(val)
			if msg != "" {
				return flat(ctx, nethttp.StatusBadRequest, msg)
			}
			psdSet, psdRemove = set, remove
		default:
			// Anything else is dropped rather than stored: the list is fixed.
		}
	}
	if len(psdSet) > 0 || len(psdRemove) > 0 {
		for _, k := range nodeOwned {
			if _, set := psdSet[k]; set && upstream.NodeKind(existing.Provider) != "" {
				return flat(ctx, nethttp.StatusBadRequest, "Field "+k+" belongs to the provider node")
			}
		}
	}

	updated := existing
	if patch.Name != nil || patch.IsActive != nil || patch.Priority != nil || len(patch.Fields) > 0 {
		if updated, err = st.Connections().Update(rc, id, patch); err != nil {
			return updateFailed(ctx, err)
		}
	}
	if len(psdSet) > 0 || len(psdRemove) > 0 {
		if err := st.Connections().PatchPSD(rc, id, psdSet, psdRemove); err != nil {
			return updateFailed(ctx, err)
		}
		if updated, _, err = st.Connections().Get(rc, id); err != nil {
			return storageDown(ctx)
		}
	}
	return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{"connection": connectionView(updated)})
}

func updateFailed(ctx contractshttp.Context, err error) contractshttp.Response {
	if errors.Is(err, repository.ErrNotFound) {
		return flat(ctx, nethttp.StatusNotFound, "Connection not found")
	}
	if errors.Is(err, document.ErrCorruptRow) {
		return flat(ctx, nethttp.StatusConflict, "Connection data is unreadable")
	}
	return flat(ctx, nethttp.StatusInternalServerError, "Failed to update connection")
}

// Delete answers DELETE /api/providers/{id}.
func (c *ProvidersController) Delete(ctx contractshttp.Context) contractshttp.Response {
	st := c.store()
	if st == nil {
		return storageDown(ctx)
	}
	deleted, err := st.Connections().Delete(ctx.Request().Origin().Context(), ctx.Request().Route("id"))
	if err != nil {
		return flat(ctx, nethttp.StatusInternalServerError, "Failed to delete connection")
	}
	if !deleted {
		return flat(ctx, nethttp.StatusNotFound, "Connection not found")
	}
	return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{"message": "Connection deleted successfully"})
}

// Overrides answers GET /api/providers/{id}/overrides.
func (c *ProvidersController) Overrides(ctx contractshttp.Context) contractshttp.Response {
	st := c.store()
	if st == nil {
		return storageDown(ctx)
	}
	canonical, ok := c.canonical(ctx.Request().Route("id"))
	if !ok {
		return flat(ctx, nethttp.StatusNotFound, "Provider not found")
	}
	set, err := st.Settings().Effective(ctx.Request().Origin().Context())
	if err != nil {
		return storageDown(ctx)
	}
	headers := map[string]string{}
	if raw, found := set.GetRaw("providerOverrides"); found {
		headers = upstream.HeaderOverrides(raw, canonical)
	}
	return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{"headers": headers, "builtinHeaders": map[string]string{}})
}

// SetOverrides answers PUT /api/providers/{id}/overrides.
func (c *ProvidersController) SetOverrides(ctx contractshttp.Context) contractshttp.Response {
	st := c.store()
	if st == nil {
		return storageDown(ctx)
	}
	canonical, ok := c.canonical(ctx.Request().Route("id"))
	if !ok {
		return flat(ctx, nethttp.StatusNotFound, "Provider not found")
	}
	var body struct {
		Headers map[string]any `json:"headers"`
	}
	if err := bindJSON(ctx, &body); err != nil {
		return flat(ctx, nethttp.StatusBadRequest, "Invalid request body")
	}
	clean, msg := cleanHeaders(body.Headers)
	if msg != "" {
		return flat(ctx, nethttp.StatusBadRequest, msg)
	}
	value := "null"
	if len(clean) > 0 {
		raw, _ := json.Marshal(map[string]any{"headers": clean})
		value = string(raw)
	}
	if err := st.Settings().MergeObjectField(ctx.Request().Origin().Context(), "providerOverrides", canonical, value); err != nil {
		return flat(ctx, nethttp.StatusInternalServerError, "Failed to save overrides")
	}
	if clean == nil {
		clean = map[string]string{}
	}
	return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{"headers": clean})
}

// canonical turns an id or alias into the registry id; a node id is its own.
func (c *ProvidersController) canonical(id string) (string, bool) {
	if upstream.NodeKind(id) != "" {
		return id, true
	}
	return c.Registry.Resolve(id)
}

// cleanHeaders validates an override payload. Empty values are dropped, and a
// payload with no headers left clears the override.
func cleanHeaders(in map[string]any) (map[string]string, string) {
	out := map[string]string{}
	for name, v := range in {
		if v == nil {
			continue
		}
		s, isString := v.(string)
		if !isString {
			return nil, "Invalid value for header " + safeName(name)
		}
		if s == "" {
			continue
		}
		if !headerName(name) {
			return nil, "Invalid header name"
		}
		if blockedHeaders[strings.ToLower(name)] {
			return nil, "Header " + name + " cannot be overridden"
		}
		if hasControl(s) {
			return nil, "Invalid value for header " + name
		}
		if len(s) > maxHeaderValue {
			return nil, "Header " + name + " value is too long"
		}
		out[name] = s
	}
	if len(out) > maxOverrideHeaders {
		return nil, "Too many headers"
	}
	if len(out) == 0 {
		return nil, ""
	}
	return out, ""
}

func headerName(n string) bool {
	if n == "" || len(n) > 128 {
		return false
	}
	for _, r := range n {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

func safeName(n string) string {
	if headerName(n) {
		return n
	}
	return "(invalid name)"
}

// hasControl reports any control character, C1 included.
func hasControl(s string) bool { return strings.IndexFunc(s, unicode.IsControl) >= 0 }

// bindJSON decodes the request body into v, refusing one over 1 MiB.
func bindJSON(ctx contractshttp.Context, v any) error {
	r := ctx.Request().Origin()
	r.Body = nethttp.MaxBytesReader(ctx.Response().Writer(), r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("more than one JSON value")
	}
	return nil
}

// cleanPSD validates a providerSpecificData object from a client and returns it
// as JSON text, or "" with a message.
func cleanPSD(raw json.RawMessage) (string, string) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", ""
	}
	if len(raw) > maxPSDBytes {
		return "", "providerSpecificData is too large"
	}
	r := gjson.ParseBytes(raw)
	if !r.IsObject() {
		return "", "providerSpecificData must be an object"
	}
	if jsonDepth(r) > maxPSDDepth {
		return "", "providerSpecificData is nested too deeply"
	}
	return string(raw), ""
}

func jsonDepth(r gjson.Result) int {
	deepest := 0
	if r.IsObject() || r.IsArray() {
		r.ForEach(func(_, v gjson.Result) bool {
			deepest = max(deepest, jsonDepth(v))
			return true
		})
		return deepest + 1
	}
	return 0
}

// splitPSD turns an update's providerSpecificData into keys to set and keys to
// remove; a null value removes.
func splitPSD(raw json.RawMessage) (set map[string]string, remove []string, msg string) {
	if _, bad := cleanPSD(raw); bad != "" {
		return nil, nil, bad
	}
	r := gjson.ParseBytes(raw)
	if !r.IsObject() {
		return nil, nil, "providerSpecificData must be an object"
	}
	set = map[string]string{}
	r.ForEach(func(k, v gjson.Result) bool {
		if v.Type == gjson.Null {
			remove = append(remove, k.String())
		} else {
			set[k.String()] = v.Raw
		}
		return true
	})
	return set, remove, ""
}
