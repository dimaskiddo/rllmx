package openai

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"rllmx/app/llm/executor"
	"rllmx/app/store/document"
)

// codexFields are the only top-level fields the Codex backend accepts; any
// other makes it refuse the whole request.
var codexFields = map[string]bool{
	"model": true, "input": true, "instructions": true, "tools": true, "tool_choice": true, "stream": true,
	"store": true, "reasoning": true, "service_tier": true, "include": true, "prompt_cache_key": true,
	"client_metadata": true, "text": true, "parallel_tool_calls": true,
}

const (
	liteHeader      = "x-openai-internal-codex-responses-lite"
	capacityMessage = "Selected model is at capacity. Please try a different model."
	busyMessage     = "The upstream is overloaded. Please try again."
)

// Codex shapes a Responses body for the ChatGPT Codex backend and watches the
// start of its answer for errors that arrive inside a 200. It holds no state
// between requests: everything a call needs is in its Request.
type Codex struct {
	Default *executor.Default
	// PeekIdle ends the look at the start of an answer when no byte arrives for
	// this long, so a quiet upstream is the stream timers' business. Zero is no
	// bound.
	PeekIdle time.Duration
}

// Execute sends one call.
func (c *Codex) Execute(ctx context.Context, req executor.Request) (executor.Response, error) {
	if req.Key == "" {
		return executor.Response{}, executor.ErrNoCredential
	}
	body, lite := shapeCodex(req)
	req.Body = body
	if req.Compact {
		req.URL = addPath(req.URL, "/compact")
	}
	if lite && req.Auth != nil {
		a := *req.Auth
		a.Headers = executor.WithHeaders(a.Headers, map[string]string{liteHeader: "true"})
		req.Auth = &a
	}
	rule := c.busyRule(req)
	for attempt := 0; ; attempt++ {
		resp, err := c.Default.Execute(ctx, req)
		if err != nil || resp.Status < 200 || resp.Status > 299 || !isEventStream(resp.Header) {
			return resp, err
		}
		verdict, rest := peekStream(ctx, resp.Body, c.PeekIdle)
		switch verdict {
		case peekClean:
			resp.Body = rest
			return resp, nil
		case peekFull:
			return failureResponse(capacityMessage), nil
		}
		if attempt >= rule.Attempts {
			return failureResponse(busyMessage), nil
		}
		if err := executor.Wait(ctx, rule.Backoff(), c.Default.Sleep); err != nil {
			return executor.Response{}, err
		}
	}
}

// busyRule is the 503 rule in force for this provider: an overloaded answer
// inside a 200 means the same as a 503 status.
func (c *Codex) busyRule(req executor.Request) executor.Rule {
	return c.Default.RetryRules(req.Entry)[http.StatusServiceUnavailable]
}

func isEventStream(h http.Header) bool {
	return strings.Contains(strings.ToLower(h.Get("Content-Type")), "text/event-stream")
}

// failureResponse is the 503 the pipeline sees in place of an answer that was
// an error under a 200 status. The text is fixed: what the upstream said is
// never kept.
func failureResponse(msg string) executor.Response {
	body := `{"error":{"message":` + document.Encode(msg) + `,"type":"server_error","code":"service_unavailable"}}`
	return executor.Response{
		Status: http.StatusServiceUnavailable,
		Header: http.Header{"Content-Type": {"application/json"}},
		Body:   io.NopCloser(strings.NewReader(body)),
	}
}

func addPath(target, suffix string) string {
	path, query, hasQuery := strings.Cut(target, "?")
	path = strings.TrimRight(path, "/") + suffix
	if hasQuery {
		return path + "?" + query
	}
	return path
}

// shapeCodex turns the Responses body the codec wrote into the one Codex
// accepts, and says whether it is a Lite request.
func shapeCodex(req executor.Request) (body []byte, lite bool) {
	b := req.Body
	b = executor.SetValue(b, "stream", true)
	b = executor.SetValue(b, "store", false)

	b, names := flattenTools(b, stripUnicodePatterns, "")
	b = fixToolChoice(b, names)

	lite = req.Spec.Lite && !hasWebSearch(b)
	if strings.TrimSpace(gjson.GetBytes(b, "instructions").String()) == "" {
		b = executor.SetValue(b, "instructions", executor.Instruction("codex"))
	}
	b = cacheKey(b, req.Session)

	b = codexReasoning(b, req.Levels, lite)
	b = codexServiceTier(b)
	if lite {
		b = liteBody(b)
	} else {
		b = executor.Remove(b, "parallel_tool_calls")
	}
	return keepOnly(b, codexFields), lite
}

// cacheKey makes the keyed session the backend's cache key. A key the client
// chose is never sent: it would let one tenant pick the cache another's
// conversation reads.
func cacheKey(b []byte, session string) []byte {
	if session == "" {
		return executor.Remove(b, "prompt_cache_key")
	}
	return executor.SetValue(b, "prompt_cache_key", session)
}

func hasWebSearch(body []byte) bool {
	for _, t := range gjson.GetBytes(body, "tools").Array() {
		if t.Get("type").String() == "web_search" {
			return true
		}
	}
	return false
}

// codexReasoning sets the effort the backend takes. A client's choice stays
// when the model accepts it; otherwise it is moved to the nearest level the
// model has, and Lite models cannot do without reasoning.
func codexReasoning(b []byte, levels []string, lite bool) []byte {
	effort := gjson.GetBytes(b, "reasoning.effort").String()
	if effort == "" {
		effort = "low"
		if lite {
			effort = "medium"
		}
	}
	effort = normalizeEffort(effort, levels, lite)
	b = executor.SetValue(b, "reasoning.effort", effort)
	if lite {
		b = executor.SetValue(b, "reasoning.context", "all_turns")
		b = executor.Remove(b, "reasoning.summary")
	} else if gjson.GetBytes(b, "reasoning.summary").String() == "" {
		b = executor.SetValue(b, "reasoning.summary", "auto")
	}
	if effort != "none" {
		return executor.SetRaw(b, "include", `["reasoning.encrypted_content"]`)
	}
	return executor.Remove(b, "include")
}

func normalizeEffort(v string, levels []string, lite bool) string {
	switch {
	case slices.Contains(levels, v):
		return v
	case lite && (v == "none" || v == "minimal"):
		return "low"
	case v == "ultra" && slices.Contains(levels, "max"):
		return "max"
	case v == "max" || v == "ultra":
		return "xhigh"
	}
	return v
}

func codexServiceTier(b []byte) []byte {
	tier := gjson.GetBytes(b, "service_tier").String()
	switch tier {
	case "fast":
		return executor.SetValue(b, "service_tier", "priority")
	case "", "priority":
		return b
	}
	return executor.Remove(b, "service_tier")
}

// liteBody moves the tools and the instructions into the input, where a Lite
// model reads them, and empties the fields they came from.
func liteBody(b []byte) []byte {
	tools := gjson.GetBytes(b, "tools")
	toolsRaw := "[]"
	if tools.IsArray() {
		toolsRaw = tools.Raw
	}
	input := gjson.GetBytes(b, "input")
	var items bytes.Buffer
	items.Grow(len(toolsRaw) + len(input.Raw) + 256)
	items.WriteString(`[{"type":"additional_tools","role":"developer","tools":`)
	items.WriteString(toolsRaw)
	items.WriteByte('}')
	if ins := strings.TrimSpace(gjson.GetBytes(b, "instructions").String()); ins != "" {
		msg, _ := sjson.Set(`{"type":"message","role":"developer","content":[{"type":"input_text"}]}`, "content.0.text", ins)
		items.WriteByte(',')
		items.WriteString(msg)
	}
	if input.IsArray() {
		for _, it := range input.Array() {
			items.WriteByte(',')
			items.WriteString(it.Raw)
		}
	}
	items.WriteByte(']')
	b = executor.SetRaw(b, "input", items.String())
	b = executor.SetValue(b, "instructions", "")
	b = executor.SetRaw(b, "tools", "null")
	if !gjson.GetBytes(b, "tool_choice").Exists() {
		b = executor.SetValue(b, "tool_choice", "auto")
	}
	return executor.SetValue(b, "parallel_tool_calls", false)
}
