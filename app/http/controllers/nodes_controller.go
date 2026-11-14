package controllers

import (
	"encoding/json"
	"errors"
	nethttp "net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/google/uuid"
	contractshttp "github.com/goravel/framework/contracts/http"

	"rllmx/app/llm/upstream"
	"rllmx/app/platform/text"
	"rllmx/app/store/document"
	"rllmx/app/store/repository"
)

const maxBaseURL = 2048

var prefixPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// Default base URLs a node takes when the client sends none.
var nodeDefaults = map[string]string{
	upstream.NodeTypeOpenAI:    "https://api.openai.com/v1",
	upstream.NodeTypeAnthropic: "https://api.anthropic.com/v1",
	upstream.NodeTypeEmbedding: "https://api.openai.com/v1",
}

// NodesController answers the provider-node routes.
type NodesController struct {
	Registry *upstream.Registry
	DB       func() *repository.Store
	Checker  *Checker
}

func (c *NodesController) store() *repository.Store {
	if c.DB == nil {
		return nil
	}
	return c.DB()
}

type nodeBody struct {
	Name    string `json:"name"`
	Prefix  string `json:"prefix"`
	APIType string `json:"apiType"`
	BaseURL string `json:"baseUrl"`
	Type    string `json:"type"`
}

// nodeView is a node as the dashboard reads it: the data fields at the top.
func nodeView(n repository.Node) map[string]any {
	d := n.Doc()
	out := map[string]any{"id": n.ID, "type": n.Type, "name": n.Name, "createdAt": n.CreatedAt, "updatedAt": n.UpdatedAt}
	for _, k := range []string{"prefix", "apiType", "baseUrl"} {
		if v, ok := d.GetString(k); ok {
			out[k] = v
		}
	}
	return out
}

// List answers GET /api/provider-nodes.
func (c *NodesController) List(ctx contractshttp.Context) contractshttp.Response {
	st := c.store()
	if st == nil {
		return storageDown(ctx)
	}
	nodes, err := st.Nodes().List(ctx.Request().Origin().Context(), "")
	if err != nil {
		return storageDown(ctx)
	}
	out := make([]map[string]any, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, nodeView(n))
	}
	return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{"nodes": out})
}

// Create answers POST /api/provider-nodes.
func (c *NodesController) Create(ctx contractshttp.Context) contractshttp.Response {
	st := c.store()
	if st == nil {
		return storageDown(ctx)
	}
	var b nodeBody
	if err := bindJSON(ctx, &b); err != nil {
		return flat(ctx, nethttp.StatusBadRequest, "Invalid request body")
	}
	typ := b.Type
	if typ == "" {
		typ = upstream.NodeTypeOpenAI
	}
	var idPrefix string
	switch typ {
	case upstream.NodeTypeOpenAI:
		if b.APIType != "chat" && b.APIType != "responses" {
			return flat(ctx, nethttp.StatusBadRequest, "Invalid OpenAI compatible API type")
		}
		idPrefix = upstream.NodeOpenAIPrefix + b.APIType + "-"
	case upstream.NodeTypeAnthropic:
		idPrefix = upstream.NodeAnthropicPrefix
	case upstream.NodeTypeEmbedding:
		idPrefix = upstream.NodeEmbeddingPrefix
	default:
		return flat(ctx, nethttp.StatusBadRequest, "Invalid provider node type")
	}
	name, prefix, base, msg := c.validate(typ, b, true)
	if msg != "" {
		return flat(ctx, nethttp.StatusBadRequest, msg)
	}

	data := map[string]any{"prefix": prefix, "baseUrl": base}
	if typ == upstream.NodeTypeOpenAI {
		data["apiType"] = b.APIType
	}
	raw, _ := json.Marshal(data)
	node, err := st.Nodes().CreateUnique(ctx.Request().Origin().Context(),
		repository.Node{ID: idPrefix + uuid.NewString(), Type: typ, Name: name, Data: string(raw)}, prefix)
	if err != nil {
		if errors.Is(err, repository.ErrPrefixTaken) {
			return flat(ctx, nethttp.StatusConflict, "Prefix is already used by another node")
		}
		return flat(ctx, nethttp.StatusInternalServerError, "Failed to create provider node")
	}
	return ctx.Response().Json(nethttp.StatusCreated, contractshttp.Json{"node": nodeView(node)})
}

// Update answers PUT /api/provider-nodes/{id}. The node and the values its
// connections copy from it change in one transaction.
func (c *NodesController) Update(ctx contractshttp.Context) contractshttp.Response {
	st := c.store()
	if st == nil {
		return storageDown(ctx)
	}
	id := ctx.Request().Route("id")
	rc := ctx.Request().Origin().Context()
	node, ok, err := st.Nodes().Get(rc, id)
	if err != nil {
		return storageDown(ctx)
	}
	if !ok {
		return flat(ctx, nethttp.StatusNotFound, "Provider node not found")
	}
	var b nodeBody
	if err := bindJSON(ctx, &b); err != nil {
		return flat(ctx, nethttp.StatusBadRequest, "Invalid request body")
	}
	if node.Type == upstream.NodeTypeOpenAI && b.APIType != "chat" && b.APIType != "responses" {
		return flat(ctx, nethttp.StatusBadRequest, "Invalid OpenAI compatible API type")
	}
	if strings.TrimSpace(b.BaseURL) == "" {
		return flat(ctx, nethttp.StatusBadRequest, "Base URL is required")
	}
	name, prefix, base, msg := c.validate(node.Type, b, false)
	if msg != "" {
		return flat(ctx, nethttp.StatusBadRequest, msg)
	}

	q := func(s string) string { return document.Encode(s) }
	patch := map[string]string{"name": q(name), "prefix": q(prefix), "baseUrl": q(base)}
	copied := map[string]string{"prefix": q(prefix), "baseUrl": q(base), "nodeName": q(name)}
	if node.Type == upstream.NodeTypeOpenAI {
		patch["apiType"] = q(b.APIType)
		copied["apiType"] = q(b.APIType)
	}
	updated, err := st.Nodes().UpdateCascade(rc, id, patch, copied, prefix)
	if err != nil {
		switch {
		case errors.Is(err, repository.ErrPrefixTaken):
			return flat(ctx, nethttp.StatusConflict, "Prefix is already used by another node")
		case errors.Is(err, repository.ErrNotFound):
			return flat(ctx, nethttp.StatusNotFound, "Provider node not found")
		}
		return flat(ctx, nethttp.StatusInternalServerError, "Failed to update provider node")
	}
	return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{"node": nodeView(updated)})
}

// Delete answers DELETE /api/provider-nodes/{id}: the node, its connections and
// its custom models go together.
func (c *NodesController) Delete(ctx contractshttp.Context) contractshttp.Response {
	st := c.store()
	if st == nil {
		return storageDown(ctx)
	}
	_, err := st.Nodes().DeleteCascade(ctx.Request().Origin().Context(), ctx.Request().Route("id"))
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return flat(ctx, nethttp.StatusNotFound, "Provider node not found")
		}
		return flat(ctx, nethttp.StatusInternalServerError, "Failed to delete provider node")
	}
	return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{"success": true})
}

// validate checks a node body and returns the clean name, prefix and base URL.
// The prefix rules apply on write only; rows already stored still read.
func (c *NodesController) validate(typ string, b nodeBody, creating bool) (name, prefix, base, msg string) {
	// Control characters are judged before any trimming: TrimSpace would strip a
	// stray U+0085 or tab at the ends and let it through.
	if text.HasControl(b.BaseURL) || text.HasControl(b.Name) || text.HasControl(b.Prefix) {
		return "", "", "", "Invalid characters in the request"
	}
	name = strings.TrimSpace(b.Name)
	if name == "" {
		return "", "", "", "Name is required"
	}
	if len(name) > maxNameBytes || text.HasControl(name) {
		return "", "", "", "Invalid name"
	}
	prefix = strings.TrimSpace(b.Prefix)
	if prefix == "" {
		return "", "", "", "Prefix is required"
	}
	if !prefixPattern.MatchString(prefix) {
		return "", "", "", "Prefix must be lower-case letters, digits and dashes, up to 32 characters"
	}
	if c.Registry.Reserved(prefix) {
		return "", "", "", "Prefix is reserved for a built-in provider"
	}
	raw := strings.TrimSpace(b.BaseURL)
	if raw == "" && creating {
		raw = nodeDefaults[typ]
	}
	base, msg = cleanBaseURL(typ, raw)
	return name, prefix, base, msg
}

// cleanBaseURL accepts an http or https address with a host and nothing that
// could carry a credential or redirect, and drops the endpoint suffix a user
// pasted so it is not appended twice.
func cleanBaseURL(typ, raw string) (string, string) {
	if len(raw) > maxBaseURL || text.HasControl(raw) {
		return "", "Invalid base URL"
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return "", "Invalid base URL"
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", "Base URL must be http or https"
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", "Base URL cannot hold credentials, a query or a fragment"
	}
	out := strings.TrimRight(u.String(), "/")
	switch typ {
	case upstream.NodeTypeAnthropic:
		out = strings.TrimSuffix(out, "/messages")
	case upstream.NodeTypeEmbedding:
		out = strings.TrimSuffix(out, "/embeddings")
	}
	return out, ""
}
