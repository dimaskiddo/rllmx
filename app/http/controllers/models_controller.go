package controllers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	nethttp "net/http"
	"strings"
	"time"

	contractshttp "github.com/goravel/framework/contracts/http"
	"github.com/goravel/framework/contracts/log"

	"rllmx/app/gateway/catalog"
	"rllmx/app/gateway/probe"
	apphttp "rllmx/app/http"
	"rllmx/app/llm/upstream"
	"rllmx/app/platform/failure"
	"rllmx/app/store/repository"
)

// ModelsController serves the model catalog to API clients.
type ModelsController struct {
	// Catalog returns the catalog, building it on first use. It fails while the
	// stores are not open.
	Catalog func() (*catalog.Catalog, error)
	// Probe tests one model through the pipeline; nil leaves the route
	// unavailable.
	Probe func() (*probe.Prober, error)
	// Registry, Caps and DB serve the dashboard's model management; nil leaves
	// those routes out.
	Registry *upstream.Registry
	Caps     *upstream.Capabilities
	DB       func() *repository.Store
	// CatalogSync runs the models.dev sync and reports it; nil leaves the
	// catalog routes answering unavailable.
	CatalogSync func() CatalogSyncer
	// Changed drops the routing view after an alias write.
	Changed func()
	Log     log.Log
}

func (m *ModelsController) list(ctx contractshttp.Context) (*catalog.Catalog, []catalog.Entry, bool) {
	r := ctx.Request().Origin()
	w := ctx.Response().Writer()
	format := failure.ForPath(r.URL.Path)
	caller, ok := apphttp.CallerFrom(ctx)
	if !ok {
		failure.Write(w, format, nethttp.StatusUnauthorized, "API key required")
		return nil, nil, false
	}
	c, err := m.Catalog()
	if err != nil || c == nil {
		if err != nil && m.Log != nil {
			m.Log.Warningf("models: catalog unavailable: %v", err)
		}
		failure.Write(w, format, nethttp.StatusServiceUnavailable, "")
		return nil, nil, false
	}
	entries, err := c.List(r.Context(), caller)
	if err != nil {
		if m.Log != nil {
			m.Log.Warningf("models: listing failed: %v", err)
		}
		failure.Write(w, format, nethttp.StatusInternalServerError, "")
		return nil, nil, false
	}
	return c, entries, true
}

func respondJSON(ctx contractshttp.Context, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		failure.Write(ctx.Response().Writer(), failure.OpenAI, nethttp.StatusInternalServerError, "")
		return
	}
	w := ctx.Response().Writer()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// List answers GET /v1/models.
func (m *ModelsController) List(ctx contractshttp.Context) contractshttp.Response {
	c, entries, ok := m.list(ctx)
	if !ok {
		return nil
	}
	data := make([]catalog.OpenAIModel, 0, len(entries))
	for _, e := range entries {
		data = append(data, e.OpenAI(c.Created))
	}
	respondJSON(ctx, nethttp.StatusOK, map[string]any{"object": "list", "data": data})
	return nil
}

// Gemini answers GET /v1beta/models.
func (m *ModelsController) Gemini(ctx contractshttp.Context) contractshttp.Response {
	_, entries, ok := m.list(ctx)
	if !ok {
		return nil
	}
	data := make([]catalog.GeminiModel, 0, len(entries))
	for _, e := range entries {
		data = append(data, e.Gemini())
	}
	respondJSON(ctx, nethttp.StatusOK, map[string]any{"models": data})
	return nil
}

// Show answers GET /v1/models/info?id= and GET /v1/models/{provider}/{model}.
func (m *ModelsController) Show(ctx contractshttp.Context) contractshttp.Response {
	r := ctx.Request().Origin()
	id, info := strings.TrimPrefix(r.URL.Path, "/v1/models/"), false
	if id == "info" {
		id, info = r.URL.Query().Get("id"), true
		if id == "" {
			failure.Write(ctx.Response().Writer(), failure.OpenAI, nethttp.StatusBadRequest, "Missing required query parameter: id")
			return nil
		}
	}
	c, entries, ok := m.list(ctx)
	if !ok {
		return nil
	}
	for _, e := range entries {
		switch {
		case e.ID != id:
		case info:
			respondJSON(ctx, nethttp.StatusOK, e.Info())
			return nil
		default:
			respondJSON(ctx, nethttp.StatusOK, e.OpenAI(c.Created))
			return nil
		}
	}
	failure.Write(ctx.Response().Writer(), failure.OpenAI, nethttp.StatusNotFound, "The model '"+id+"' does not exist or you do not have access to it.")
	return nil
}

// tag is one entry of GET /api/tags, Ollama's model list shape.
type tag struct {
	Name       string    `json:"name"`
	Model      string    `json:"model"`
	ModifiedAt string    `json:"modified_at"`
	Size       int64     `json:"size"`
	Digest     string    `json:"digest"`
	Details    tagDetail `json:"details"`
}

type tagDetail struct {
	Format            string `json:"format"`
	Family            string `json:"family"`
	ParameterSize     string `json:"parameter_size"`
	QuantizationLevel string `json:"quantization_level"`
}

// Tags answers GET /api/tags: the caller's own catalog in Ollama's list shape,
// so an Ollama client pointed at this router lists what it may call. The
// reference answers a fixed fake list behind the dashboard, where no Ollama
// client can reach it.
func (m *ModelsController) Tags(ctx contractshttp.Context) contractshttp.Response {
	c, entries, ok := m.list(ctx)
	if !ok {
		return nil
	}
	modified := time.Unix(c.Created, 0).UTC().Format(time.RFC3339)
	out := make([]tag, 0, len(entries))
	for _, e := range entries {
		sum := sha256.Sum256([]byte(e.ID))
		family := e.Provider
		if family == "" {
			family = e.OwnedBy
		}
		out = append(out, tag{
			Name: e.ID, Model: e.ID, ModifiedAt: modified, Digest: hex.EncodeToString(sum[:]),
			Details: tagDetail{Format: "safetensors", Family: family},
		})
	}
	respondJSON(ctx, nethttp.StatusOK, map[string]any{"models": out})
	return nil
}

// Test answers POST /api/models/test: one named model, through the pipeline
// with the normal account selection. Only chat models: a media model answers
// through its own route and its own probe.
func (m *ModelsController) Test(ctx contractshttp.Context) contractshttp.Response {
	var b struct {
		Model string `json:"model"`
		Kind  string `json:"kind"`
	}
	if err := bindJSON(ctx, &b); err != nil {
		return flat(ctx, nethttp.StatusBadRequest, "Invalid request body")
	}
	if b.Model == "" {
		return flat(ctx, nethttp.StatusBadRequest, "Model required")
	}
	if b.Kind != "" && b.Kind != "llm" {
		return flat(ctx, nethttp.StatusBadRequest, "Only llm models can be tested")
	}
	if m.Probe == nil {
		return flat(ctx, nethttp.StatusServiceUnavailable, "Storage is not available")
	}
	p, err := m.Probe()
	if err != nil {
		return flat(ctx, nethttp.StatusServiceUnavailable, "Storage is not available")
	}
	r := p.Model(ctx.Request().Origin().Context(), b.Model)
	out := contractshttp.Json{"ok": r.OK, "latencyMs": r.LatencyMs, "status": r.Status}
	if !r.OK {
		out["error"] = r.Error
	}
	return ctx.Response().Json(nethttp.StatusOK, out)
}
