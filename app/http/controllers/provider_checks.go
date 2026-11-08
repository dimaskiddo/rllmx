package controllers

import (
	"errors"
	nethttp "net/http"
	"strings"
	"time"

	contractshttp "github.com/goravel/framework/contracts/http"

	"rllmx/app/catalog"
	"rllmx/app/document"
	"rllmx/app/upstream"
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
}

// verdict is the wire form of a check: valid, or one fixed message.
func verdict(r Result) contractshttp.Json {
	out := contractshttp.Json{"valid": r.Valid()}
	if !r.Valid() {
		out["error"] = r.Outcome.Message()
	}
	if r.Method != "" {
		out["method"] = r.Method
	}
	if r.Dimensions > 0 {
		out["dimensions"] = r.Dimensions
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
	if hasControl(b.Provider) || hasControl(b.ModelID) || len(b.ModelID) > maxNameBytes || len(b.APIKey) > maxSecretBytes || hasControl(b.APIKey) {
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
	key, _ := conn.Doc().GetString("apiKey")
	started := time.Now()

	var res Result
	switch {
	case upstream.NodeKind(conn.Provider) != "":
		node, found, nerr := st.Nodes().Get(rc, conn.Provider)
		if nerr != nil {
			return storageDown(ctx)
		}
		if !found {
			return flat(ctx, nethttp.StatusNotFound, "Provider node not found")
		}
		base, _ := node.Doc().GetString("baseUrl")
		model, _ := conn.Doc().GetString("defaultModel")
		res = c.Checker.CheckNode(rc, Node{Type: node.Type, BaseURL: base}, key, model, true)
	case conn.AuthType != "apikey":
		res = Result{Outcome: OutcomeUnsupported}
	default:
		entry, found := c.Registry.ByID(conn.Provider)
		if !found {
			res = Result{Outcome: OutcomeUnsupported}
		} else {
			res = c.Checker.CheckRegistry(rc, entry, key)
		}
	}

	at := time.Now()
	if res.Outcome != OutcomeUnsupported {
		if err := st.Connections().RecordTest(rc, id, res.Valid(), res.Outcome.Message(), at); err != nil {
			if errors.Is(err, document.ErrCorruptRow) {
				return flat(ctx, nethttp.StatusConflict, "Connection data is unreadable")
			}
			return flat(ctx, nethttp.StatusInternalServerError, "Failed to record the test")
		}
	}
	out := verdict(res)
	out["latencyMs"] = at.Sub(started).Milliseconds()
	out["testedAt"] = at.UTC().Format(time.RFC3339)
	return ctx.Response().Json(nethttp.StatusOK, out)
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
	return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{"provider": conn.Provider, "connectionId": conn.ID, "models": models})
}

// ValidateNode answers POST /api/provider-nodes/validate: does this address and
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
	if hasControl(b.BaseURL) || hasControl(b.APIKey) || hasControl(b.ModelID) || len(b.ModelID) > maxNameBytes || len(b.APIKey) > maxSecretBytes {
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
