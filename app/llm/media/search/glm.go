package search

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
)

// GLM searches through Z.ai's web_search_prime tool over its MCP endpoint.
type GLM struct{}

// Kind is the service it serves.
func (GLM) Kind() string { return upstream.KindSearch }

// Build makes the JSON-RPC call the tool takes.
func (GLM) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	payload := map[string]any{
		"jsonrpc": "2.0",
		"id":      "rllmx-" + strconv.FormatInt(time.Now().UnixNano(), 10),
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "web_search_prime",
			"arguments": map[string]any{"search_query": field(r, "query"), "count": count(r, "max_results")},
		},
	}
	return call(http.MethodPost, base(r, "https://api.z.ai/api/mcp/web_search_prime/mcp"), headers(r, nil), payload)
}

// Parse reads the tool's answer, whose payload is JSON carried as text.
func (GLM) Parse(_ context.Context, r media.Request, _ media.Client, status int, _ http.Header, raw []byte) (media.Response, error) {
	if status < 200 || status > 299 {
		return media.Response{}, refused(status)
	}
	payload := gjson.ParseBytes(raw)
	if text := gjson.GetBytes(raw, "result.content.0.text"); text.Type == gjson.String {
		if json.Valid([]byte(text.String())) {
			payload = gjson.Parse(text.String())
		}
	}
	items := payload.Get("results")
	if !items.Exists() {
		items = payload.Get("news")
	}
	if !items.Exists() && payload.IsArray() {
		items = payload
	}
	out := []result{}
	for i, v := range items.Array() {
		if i >= count(r, "max_results") {
			break
		}
		out = append(out, item("glm", i+1, v.Get("title").String(), firstOf(v.Get("link").String(), v.Get("url").String())).
			withSnippet(v.Get("content").String()).
			withPublished(firstOf(v.Get("publish_date").String(), v.Get("published_at").String())).
			withFavicon(v.Get("icon").String()).
			withSource(v.Get("media").String()))
	}
	total := len(out)
	return reply("glm", field(r, "query"), out, nil, usage{QueriesUsed: 1, SearchCost: cost(r)}, &total, nil)
}
