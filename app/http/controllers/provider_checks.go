package controllers

import (
	"context"
	"errors"
	nethttp "net/http"
	"strings"
	"time"

	contractshttp "github.com/goravel/framework/contracts/http"
	"golang.org/x/sync/errgroup"

	"rllmx/app/gateway/catalog"
	"rllmx/app/llm/oauth"
	"rllmx/app/llm/upstream"
	"rllmx/app/platform/text"
	"rllmx/app/store/document"
	"rllmx/app/store/repository"
)

// outcomeStatus is the HTTP status a failed model-list fetch answers with.
var outcomeStatus = map[Outcome]int{
	OutcomeInvalidKey:    nethttp.StatusUnauthorized,
	OutcomeBlocked:       nethttp.StatusBadGateway,
	OutcomeNoConnect:     nethttp.StatusBadGateway,
	OutcomeTimedOut:      nethttp.StatusGatewayTimeout,
	OutcomeCertificate:   nethttp.StatusBadGateway,
	OutcomeRedirected:    nethttp.StatusBadGateway,
	OutcomeTooLarge:      nethttp.StatusBadGateway,
	OutcomeNotFound:      nethttp.StatusBadGateway,
	OutcomeProviderError: nethttp.StatusBadGateway,
	OutcomeUnexpected:    nethttp.StatusBadGateway,
	OutcomeUnsupported:   nethttp.StatusNotImplemented,
	OutcomeReconnect:     nethttp.StatusConflict,
	OutcomeRefreshFailed: nethttp.StatusBadGateway,
	OutcomeNoToken:       nethttp.StatusConflict,
}

// verdict is the wire form of a check: valid, or one fixed message.
func verdict(r Result) contractshttp.Json {
	out := contractshttp.Json{"valid": r.Valid()}
	if !r.Valid() {
		out["error"] = r.Outcome.Message()
	}
	if r.Outcome == OutcomeUnsupported {
		// A caller that gates a save on the answer must not read "cannot be
		// checked" as "the key is bad".
		out["unsupported"] = true
	}
	if r.Method != "" {
		out["method"] = r.Method
	}
	if r.Dimensions > 0 {
		out["dimensions"] = r.Dimensions
	}
	if r.Warning {
		out["warning"] = true
	}
	return out
}

type validateBody struct {
	Provider string `json:"provider"`
	APIKey   string `json:"apiKey"`
	ModelID  string `json:"modelId"`
}

// Validate answers POST /api/providers/validate: does this key work for this
// provider. A node is probed at the base URL it is stored with, never one the
// caller sent. Nothing is written.
func (c *ProvidersController) Validate(ctx contractshttp.Context) contractshttp.Response {
	st := c.store()
	if st == nil || c.Checker == nil {
		return storageDown(ctx)
	}
	var b validateBody
	if err := bindJSON(ctx, &b); err != nil {
		return flat(ctx, nethttp.StatusBadRequest, "Invalid request body")
	}
	if text.HasControl(b.Provider) || text.HasControl(b.ModelID) || len(b.ModelID) > maxNameBytes || len(b.APIKey) > maxSecretBytes || text.HasControl(b.APIKey) {
		return flat(ctx, nethttp.StatusBadRequest, "Invalid request body")
	}
	provider := strings.TrimSpace(b.Provider)
	if id, ok := c.Registry.Resolve(provider); ok {
		provider = id
	}
	rc := ctx.Request().Origin().Context()
	if kind := upstream.NodeKind(provider); kind != "" {
		node, found, err := st.Nodes().Get(rc, provider)
		if err != nil {
			return storageDown(ctx)
		}
		if !found {
			return flat(ctx, nethttp.StatusNotFound, "Provider node not found")
		}
		base, _ := node.Doc().GetString("baseUrl")
		return ctx.Response().Json(nethttp.StatusOK, verdict(c.Checker.CheckNode(rc, Node{Type: node.Type, BaseURL: base}, b.APIKey, b.ModelID, false)))
	}
	entry, ok := c.Registry.ByID(provider)
	if !ok {
		return flat(ctx, nethttp.StatusBadRequest, "Invalid provider")
	}
	if entry.Category == upstream.CategoryCookie {
		return ctx.Response().Json(nethttp.StatusOK, verdict(c.Checker.CheckWeb(rc, entry.ID, b.APIKey)))
	}
	if entry.ID == "deepseek-web" {
		return ctx.Response().Json(nethttp.StatusOK, verdict(c.Checker.CheckDeepSeekWeb(rc, b.APIKey)))
	}
	return ctx.Response().Json(nethttp.StatusOK, verdict(c.Checker.CheckRegistry(rc, entry, b.APIKey)))
}

// Test answers POST /api/providers/{id}/test: check a saved connection with its
// stored key and record the outcome on it. A provider the generic prober cannot
// judge answers "test not supported yet" and the row is not touched.
func (c *ProvidersController) Test(ctx contractshttp.Context) contractshttp.Response {
	st := c.store()
	if st == nil || c.Checker == nil {
		return storageDown(ctx)
	}
	id := ctx.Request().Route("id")
	conn, ok, err := st.Connections().Get(ctx.Request().Origin().Context(), id)
	if err != nil {
		return storageDown(ctx)
	}
	if !ok {
		return flat(ctx, nethttp.StatusNotFound, "Connection not found")
	}
	res, status, msg := c.checkOne(ctx.Request().Origin().Context(), st, conn)
	if status != 0 {
		return flat(ctx, status, msg)
	}
	out := verdict(res.result)
	if res.refreshed {
		out["refreshed"] = true
	}
	out["latencyMs"] = res.latency
	out["testedAt"] = res.at.UTC().Format(time.RFC3339)
	return ctx.Response().Json(nethttp.StatusOK, out)
}

// checkResult is what one connection's key check came to.
type checkResult struct {
	result    Result
	refreshed bool
	latency   int64
	at        time.Time
}

// checkOne runs the key check and records the outcome. A non-zero status is a
// failure with that HTTP status and message; the single route and the batch
// share both rules.
func (c *ProvidersController) checkOne(ctx context.Context, st *repository.Store, conn repository.Connection) (checkResult, int, string) {
	if conn.Corrupt {
		return checkResult{}, nethttp.StatusConflict, "Connection data is unreadable"
	}
	key, _ := conn.Doc().GetString("apiKey")
	started := time.Now()

	var res Result
	refreshed := false
	switch {
	case upstream.NodeKind(conn.Provider) != "":
		node, found, nerr := st.Nodes().Get(ctx, conn.Provider)
		if nerr != nil {
			return checkResult{}, nethttp.StatusServiceUnavailable, "Storage is not available"
		}
		if !found {
			return checkResult{}, nethttp.StatusNotFound, "Provider node not found"
		}
		base, _ := node.Doc().GetString("baseUrl")
		model, _ := conn.Doc().GetString("defaultModel")
		res = c.Checker.CheckNode(ctx, Node{Type: node.Type, BaseURL: base}, key, model, true)
	case conn.AuthType == "oauth" && c.oauthEntry(conn.Provider):
		res, refreshed = c.checkLogin(ctx, conn.ID)
	case conn.AuthType == "cookie":
		res = c.Checker.CheckWeb(ctx, conn.Provider, key)
	case conn.Provider == "deepseek-web":
		res = c.Checker.CheckDeepSeekWeb(ctx, key)
	case conn.AuthType != "apikey":
		res = Result{Outcome: OutcomeUnsupported}
	default:
		entry, found := c.Registry.ByID(conn.Provider)
		if !found {
			res = Result{Outcome: OutcomeUnsupported}
		} else {
			res = c.Checker.CheckRegistry(ctx, entry, key)
		}
	}

	at := time.Now()
	if res.Outcome != OutcomeUnsupported {
		if err := st.Connections().RecordTest(ctx, conn.ID, res.Valid(), res.Outcome.Message(), at); err != nil {
			if errors.Is(err, document.ErrCorruptRow) {
				return checkResult{}, nethttp.StatusConflict, "Connection data is unreadable"
			}
			return checkResult{}, nethttp.StatusInternalServerError, "Failed to record the test"
		}
	}
	return checkResult{result: res, refreshed: refreshed, latency: at.Sub(started).Milliseconds(), at: at}, 0, ""
}

// ModelsList answers GET /api/providers/{id}/models. A node connection asks the
// node's own list with the saved key; a registry provider answers from the
// embedded list, its custom models added and its disabled ones removed, without
// a request.
func (c *ProvidersController) ModelsList(ctx contractshttp.Context) contractshttp.Response {
	st := c.store()
	if st == nil || c.Checker == nil {
		return storageDown(ctx)
	}
	id := ctx.Request().Route("id")
	rc := ctx.Request().Origin().Context()
	conn, ok, err := st.Connections().Get(rc, id)
	if err != nil {
		return storageDown(ctx)
	}
	if !ok {
		return flat(ctx, nethttp.StatusNotFound, "Connection not found")
	}
	if conn.Corrupt {
		return flat(ctx, nethttp.StatusConflict, "Connection data is unreadable")
	}

	if upstream.NodeKind(conn.Provider) != "" {
		node, found, nerr := st.Nodes().Get(rc, conn.Provider)
		if nerr != nil {
			return storageDown(ctx)
		}
		if !found {
			return flat(ctx, nethttp.StatusNotFound, "Provider node not found")
		}
		base, _ := node.Doc().GetString("baseUrl")
		key, _ := conn.Doc().GetString("apiKey")
		list, out := c.Checker.ListNodeModels(rc, Node{Type: node.Type, BaseURL: base}, key)
		if out != OutcomeValid {
			return flat(ctx, outcomeStatus[out], "Failed to fetch models: "+out.Message())
		}
		return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{"provider": conn.Provider, "connectionId": conn.ID, "models": list})
	}

	entry, found := c.Registry.ByID(conn.Provider)
	if !found {
		return flat(ctx, nethttp.StatusBadRequest, "Provider does not support models listing")
	}
	models, err := catalog.RegistryModels(rc, st.KV(), entry)
	if err != nil {
		return storageDown(ctx)
	}
	// An OAuth account's provider can name its live list; the embedded one is the
	// fallback when it cannot be read.
	if c.LiveCatalog != nil && (conn.AuthType == "oauth" || conn.AuthType == "access_token") {
		if ids, live := c.LiveCatalog.Live(rc, conn.Provider, conn); live {
			if fresh, lerr := catalog.LiveList(rc, st.KV(), entry, ids); lerr == nil {
				models = fresh
			}
		}
	}
	return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{"provider": conn.Provider, "connectionId": conn.ID, "models": models})
}

// Validate answers POST /api/provider-nodes/validate: does this address and
// key work, before a node exists. The address passes the same checks as a
// node's, and the dial guard decides about private addresses at connect time.
func (c *NodesController) Validate(ctx contractshttp.Context) contractshttp.Response {
	if c.Checker == nil {
		return storageDown(ctx)
	}
	var b struct {
		BaseURL string `json:"baseUrl"`
		APIKey  string `json:"apiKey"`
		Type    string `json:"type"`
		ModelID string `json:"modelId"`
	}
	if err := bindJSON(ctx, &b); err != nil {
		return flat(ctx, nethttp.StatusBadRequest, "Invalid request body")
	}
	if strings.TrimSpace(b.BaseURL) == "" || strings.TrimSpace(b.APIKey) == "" {
		return flat(ctx, nethttp.StatusBadRequest, "Base URL and API key required")
	}
	if text.HasControl(b.BaseURL) || text.HasControl(b.APIKey) || text.HasControl(b.ModelID) || len(b.ModelID) > maxNameBytes || len(b.APIKey) > maxSecretBytes {
		return flat(ctx, nethttp.StatusBadRequest, "Invalid request body")
	}
	typ := b.Type
	if typ == "" {
		typ = upstream.NodeTypeOpenAI
	}
	switch typ {
	case upstream.NodeTypeOpenAI, upstream.NodeTypeAnthropic, upstream.NodeTypeEmbedding:
	default:
		return flat(ctx, nethttp.StatusBadRequest, "Invalid provider node type")
	}
	base, msg := cleanBaseURL(typ, strings.TrimSpace(b.BaseURL))
	if msg != "" {
		return flat(ctx, nethttp.StatusBadRequest, msg)
	}
	if typ == upstream.NodeTypeEmbedding && strings.TrimSpace(b.ModelID) == "" {
		return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{"valid": false, "error": "Model ID required for embedding validation"})
	}
	res := c.Checker.CheckNode(ctx.Request().Origin().Context(), Node{Type: typ, BaseURL: base}, b.APIKey, strings.TrimSpace(b.ModelID), false)
	return ctx.Response().Json(nethttp.StatusOK, verdict(res))
}

// oauthEntry reports whether a registry provider has a login, so its OAuth
// accounts can be checked here.
func (c *ProvidersController) oauthEntry(provider string) bool {
	e, ok := c.Registry.ByID(provider)
	return ok && e.OAuth != nil && c.Refresher != nil
}

// checkLogin checks an OAuth account without asking the provider: it renews the
// token when it is due, then looks for one that has not run out. The outcome is
// one of a few fixed words.
func (c *ProvidersController) checkLogin(ctx context.Context, id string) (Result, bool) {
	refreshed, err := c.Refresher.Ensure(ctx, id)
	switch {
	case err == nil:
		return Result{Outcome: OutcomeValid, Method: "refresh"}, refreshed
	case errors.Is(err, oauth.ErrExpired):
		return Result{Outcome: OutcomeReconnect}, false
	case errors.Is(err, oauth.ErrNoToken):
		return Result{Outcome: OutcomeNoToken}, false
	case errors.Is(err, oauth.ErrStorage):
		return Result{Outcome: OutcomeRefreshFailed}, false
	}
	return Result{Outcome: OutcomeRefreshFailed}, false
}

// batchModes are the connection groups POST /api/providers/test-batch accepts.
var batchModes = map[string]bool{
	"provider": true, "oauth": true, "free": true, "apikey": true, "compatible": true, "all": true,
}

const (
	// batchParallel is how many connections one batch tests at once.
	batchParallel = 3
	batchTimeout  = 5 * time.Minute
	modelsTimeout = 2 * time.Minute
)

type batchBody struct {
	Mode       string `json:"mode"`
	ProviderID string `json:"providerId"`
}

// TestBatch answers POST /api/providers/test-batch: every active connection in
// one group, each checked and recorded exactly as the single route does.
func (c *ProvidersController) TestBatch(ctx contractshttp.Context) contractshttp.Response {
	st := c.store()
	if st == nil || c.Checker == nil {
		return storageDown(ctx)
	}
	var b batchBody
	if err := bindJSON(ctx, &b); err != nil {
		return flat(ctx, nethttp.StatusBadRequest, "Invalid request body")
	}
	if !batchModes[b.Mode] {
		return flat(ctx, nethttp.StatusBadRequest, "Invalid mode")
	}
	if b.Mode == "provider" && b.ProviderID == "" {
		return flat(ctx, nethttp.StatusBadRequest, "providerId is required for this mode")
	}
	rc := ctx.Request().Origin().Context()
	all, err := st.Connections().List(rc, repository.ConnFilter{})
	if err != nil {
		return storageDown(ctx)
	}
	conns := make([]repository.Connection, 0, len(all))
	for _, conn := range all {
		if conn.IsActive && c.inBatch(b, conn) {
			conns = append(conns, conn)
		}
	}
	bctx, cancel := context.WithTimeout(rc, batchTimeout)
	defer cancel()
	rows := make([]contractshttp.Json, len(conns))
	var g errgroup.Group
	g.SetLimit(batchParallel)
	for i, conn := range conns {
		g.Go(func() error {
			rows[i] = c.batchRow(bctx, st, conn)
			return nil
		})
	}
	_ = g.Wait()
	passed := 0
	for _, row := range rows {
		if row["valid"] == true {
			passed++
		}
	}
	return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{
		"mode": b.Mode, "providerId": b.ProviderID, "results": rows,
		"testedAt": time.Now().UTC().Format(time.RFC3339),
		"summary":  contractshttp.Json{"total": len(rows), "passed": passed, "failed": len(rows) - passed},
	})
}

// inBatch reports whether one connection belongs to the requested group.
func (c *ProvidersController) inBatch(b batchBody, conn repository.Connection) bool {
	node := upstream.NodeKind(conn.Provider) != ""
	switch b.Mode {
	case "all":
		return true
	case "compatible":
		return node
	case "provider":
		return conn.Provider == b.ProviderID
	case "oauth":
		return !node && conn.AuthType == "oauth"
	case "apikey":
		return !node && conn.AuthType == "apikey"
	case "free":
		e, ok := c.Registry.ByID(conn.Provider)
		return !node && ok && (e.Category == upstream.CategoryFree || e.Category == upstream.CategoryFreeTier)
	}
	return false
}

// batchRow is one connection's line in a batch answer. A connection that could
// not be checked is that line's failure, never the whole request's.
func (c *ProvidersController) batchRow(ctx context.Context, st *repository.Store, conn repository.Connection) contractshttp.Json {
	row := contractshttp.Json{
		"provider": conn.Provider, "connectionId": conn.ID, "connectionName": conn.Name,
		"authType": conn.AuthType, "valid": false,
	}
	res, status, msg := c.checkOne(ctx, st, conn)
	if status != 0 {
		row["error"] = msg
		return row
	}
	if res.result.Valid() {
		row["valid"] = true
	} else {
		row["error"] = res.result.Outcome.Message()
	}
	row["latencyMs"] = res.latency
	row["testedAt"] = res.at.UTC().Format(time.RFC3339)
	return row
}

// TestModels answers POST /api/providers/{id}/test-models: a forced probe of the
// connection's own models, up to the list ceiling.
func (c *ProvidersController) TestModels(ctx contractshttp.Context) contractshttp.Response {
	st := c.store()
	if st == nil || c.Probe == nil {
		return storageDown(ctx)
	}
	rc := ctx.Request().Origin().Context()
	conn, ok, err := st.Connections().Get(rc, ctx.Request().Route("id"))
	if err != nil {
		return storageDown(ctx)
	}
	if !ok {
		return flat(ctx, nethttp.StatusNotFound, "Connection not found")
	}
	if conn.Corrupt {
		return flat(ctx, nethttp.StatusConflict, "Connection data is unreadable")
	}
	p, err := c.Probe()
	if err != nil {
		return storageDown(ctx)
	}
	pctx, cancel := context.WithTimeout(rc, modelsTimeout)
	defer cancel()
	results, err := p.All(pctx, conn)
	if err != nil {
		return flat(ctx, nethttp.StatusBadRequest, "This connection's models cannot be listed")
	}
	if len(results) == 0 {
		return flat(ctx, nethttp.StatusBadRequest, "This connection names no model to test")
	}
	return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{
		"provider": conn.Provider, "connectionId": conn.ID, "results": results,
	})
}
