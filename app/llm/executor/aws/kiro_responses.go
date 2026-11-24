package aws

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/stream"
	"rllmx/app/llm/upstream"
)

const (
	// rateExceeded is what the IDE transport answers an account that is enrolled
	// in the Responses transport, even with quota left.
	rateExceeded = "USER_REQUEST_RATE_EXCEEDED"
	// createResponseWait is how long the Responses transport's own rate limit is
	// given to clear: it admits one request at a time.
	createResponseWait = 1500 * time.Millisecond
	// maxResponsesText bounds the text of an error the upstream reports inside
	// its stream before it reaches a client.
	maxResponsesText = 512
	// kiroOrigin is the marker the editor client sends.
	kiroOrigin = "AI_EDITOR"
)

// createResponseURL is the Responses transport's address. The region joins the
// host that receives the token, so only a region the runtime serves gets in.
func createResponseURL(region string) string {
	return "https://runtime." + kiroServedRegion(region, "runtime") + ".kiro.dev/v1/responses"
}

// createResponseHeaders speak as the IDE's editor client. An API key is its own
// kind of credential and is not an SSO bearer.
func createResponseHeaders(auth kiroAuth, profile string) map[string]string {
	h := map[string]string{
		"Content-Type":                "application/json",
		"Accept":                      "text/event-stream",
		"Authorization":               "Bearer " + auth.token,
		"x-amzn-kiro-origin":          kiroOrigin,
		"x-amzn-codewhisperer-optout": "true",
		"User-Agent":                  upstream.KiroACPAgent,
		"x-amzn-kiro-attempt":         "1;max=3",
	}
	if profile != "" {
		h["x-amzn-kiro-profile"] = profile
	}
	switch auth.method {
	case "api_key":
		h["TokenType"] = "API_KEY"
	case "external_idp":
		h["TokenType"] = "EXTERNAL_IDP"
		h["x-amz-sso-bearer"] = auth.token
	default:
		h["x-amz-sso-bearer"] = auth.token
	}
	return h
}

// buildCreateResponse rewrites a conversation as the Responses transport reads
// it: the turns as input items, the tools as functions, the thinking request as
// reasoning. The conversation is the one the Kiro codec already repaired, so
// nothing is judged again here.
func buildCreateResponse(body []byte, model, profile string) []byte {
	g := gjson.ParseBytes(body)
	state := g.Get("conversationState")
	current := state.Get("currentMessage")
	input := []any{}
	for _, turn := range append(state.Get("history").Array(), current) {
		input = appendUserItems(input, turn.Get("userInputMessage"))
		input = appendAssistantItems(input, turn.Get("assistantResponseMessage"))
	}

	meta := map[string]any{"kiro:agent_mode": "vibe"}
	if id := state.Get("conversationId").String(); id != "" {
		meta["kiro:conversation_id"] = id
	}
	user := current.Get("userInputMessage")
	out := map[string]any{
		"model":    cmp.Or(user.Get("modelId").String(), model),
		"input":    input,
		"stream":   true,
		"origin":   cmp.Or(user.Get("origin").String(), kiroOrigin),
		"metadata": meta,
	}
	if tools := createResponseTools(user.Get("userInputMessageContext.tools")); len(tools) > 0 {
		out["tools"] = tools
	}
	if r := createResponseReasoning(g.Get("additionalModelRequestFields")); len(r) > 0 {
		out["reasoning"] = r
	}
	if profile != "" {
		out["profileArn"] = profile
	}
	raw, _ := json.Marshal(out)
	return raw
}

func appendUserItems(input []any, u gjson.Result) []any {
	if !u.Exists() {
		return input
	}
	for _, r := range u.Get("userInputMessageContext.toolResults").Array() {
		var texts []string
		for _, c := range r.Get("content").Array() {
			texts = append(texts, c.Get("text").String())
		}
		input = append(input, map[string]any{"type": "function_call_output", "call_id": r.Get("toolUseId").String(), "output": strings.Join(texts, "\n")})
	}
	var images []any
	for _, img := range u.Get("images").Array() {
		data := img.Get("source.bytes").String()
		if data == "" {
			continue
		}
		format := strings.ToLower(cmp.Or(img.Get("format").String(), "png"))
		images = append(images, map[string]any{"type": "input_image", "image_url": "data:image/" + format + ";base64," + data})
	}
	text := u.Get("content").String()
	switch {
	case len(images) > 0:
		parts := []any{}
		if text != "" {
			parts = append(parts, map[string]any{"type": "input_text", "text": text})
		}
		input = append(input, map[string]any{"role": "user", "content": append(parts, images...)})
	case text != "":
		input = append(input, map[string]any{"role": "user", "content": text})
	}
	return input
}

func appendAssistantItems(input []any, a gjson.Result) []any {
	if !a.Exists() {
		return input
	}
	if text := a.Get("content").String(); text != "" {
		input = append(input, map[string]any{"role": "assistant", "content": text})
	}
	for _, c := range a.Get("toolUses").Array() {
		input = append(input, map[string]any{"type": "function_call", "call_id": c.Get("toolUseId").String(), "name": c.Get("name").String(), "arguments": callArguments(c.Get("input"))})
	}
	return input
}

// callArguments keeps a call's arguments as JSON text, which is what the
// Responses wire carries.
func callArguments(in gjson.Result) string {
	switch {
	case in.Type == gjson.String && strings.TrimSpace(in.String()) != "":
		return in.String()
	case in.IsObject() || in.IsArray():
		return in.Raw
	}
	return "{}"
}

func createResponseTools(specs gjson.Result) []any {
	var out []any
	for _, t := range specs.Array() {
		s := t.Get("toolSpecification")
		name := s.Get("name").String()
		if name == "" {
			continue
		}
		params := json.RawMessage(`{}`)
		if schema := s.Get("inputSchema.json"); schema.Exists() {
			params = json.RawMessage(schema.Raw)
		}
		out = append(out, map[string]any{"type": "function", "name": name, "description": s.Get("description").String(), "parameters": params})
	}
	return out
}

func createResponseReasoning(f gjson.Result) map[string]any {
	out := map[string]any{}
	if v := f.Get("thinking.type").String(); v != "" {
		out["type"] = v
	}
	if v := cmp.Or(f.Get("output_config.effort").String(), f.Get("reasoning.effort").String()); v != "" {
		out["effort"] = v
	}
	if v := f.Get("thinking.display").String(); v != "" {
		out["summary"] = v
	}
	return out
}

// responsesAdapter reads the Responses transport's events and writes them as the
// events the Kiro codec reads, so the codec stays the one place a Kiro turn is
// understood. It holds one event's worth of output at a time.
type responsesAdapter struct {
	src  *stream.Reader
	body io.Closer
	out  bytes.Buffer
	done bool
	// cur is the call whose arguments are streaming, and streamed whether any
	// arrived, so the closing item does not repeat them.
	cur      string
	streamed bool
	calls    int
	refused  bool
}

// adaptResponses wraps an upstream Responses stream.
func adaptResponses(body io.ReadCloser) io.ReadCloser {
	return &responsesAdapter{src: stream.NewReader(body), body: body}
}

func (a *responsesAdapter) Read(p []byte) (int, error) {
	for a.out.Len() == 0 {
		if a.done {
			return 0, io.EOF
		}
		a.step()
	}
	return a.out.Read(p)
}

func (a *responsesAdapter) Close() error { return a.body.Close() }

func (a *responsesAdapter) step() {
	ev, err := a.src.Next()
	switch {
	case errors.Is(err, io.EOF):
		a.done = true
	case err != nil:
		a.fail("the upstream stream could not be read")
	default:
		a.event(ev.Data)
	}
}

func (a *responsesAdapter) emit(name, payload string) {
	a.out.Write(appendEvent(nil, name, []byte(payload)))
}

func (a *responsesAdapter) fail(msg string) {
	raw, _ := json.Marshal(map[string]any{"error": map[string]any{"message": msg, "type": "api_error"}})
	a.emit("error", string(raw))
	a.done = true
}

func (a *responsesAdapter) text(name, field, text string) {
	raw, _ := json.Marshal(map[string]string{field: text})
	a.emit(name, string(raw))
}

func (a *responsesAdapter) toolUse(id, name, input string) {
	raw, _ := json.Marshal(map[string]any{"toolUseEvent": map[string]string{"toolUseId": id, "name": name, "input": input}})
	a.emit("toolUseEvent", string(raw))
}

func (a *responsesAdapter) event(data []byte) {
	g := gjson.ParseBytes(data)
	if !g.IsObject() {
		return
	}
	delta := g.Get("delta").String()
	switch g.Get("type").String() {
	case "response.output_text.delta":
		a.text("assistantResponseEvent", "content", delta)
	case "response.reasoning_text.delta", "response.reasoning.delta":
		a.text("reasoningContentEvent", "text", delta)
	case "response.refusal.delta":
		a.refused = true
		a.text("assistantResponseEvent", "content", delta)
	case "response.refusal.done":
		if r := g.Get("refusal").String(); r != "" && !a.refused {
			a.text("assistantResponseEvent", "content", r)
		}
	case "response.output_item.added":
		if item := g.Get("item"); item.Get("type").String() == "function_call" {
			a.cur, a.streamed = cmp.Or(item.Get("call_id").String(), item.Get("id").String()), false
			a.calls++
			a.toolUse(a.cur, item.Get("name").String(), "")
		}
	case "response.function_call_arguments.delta":
		if a.cur != "" {
			a.streamed = true
			a.toolUse(a.cur, "", delta)
		}
	case "response.output_item.done":
		item := g.Get("item")
		id := cmp.Or(item.Get("call_id").String(), item.Get("id").String())
		if arguments := item.Get("arguments").String(); item.Get("type").String() == "function_call" && arguments != "" && !(id == a.cur && a.streamed) {
			a.toolUse(id, "", arguments)
		}
	case "response.completed", "response.incomplete":
		a.finish(g)
	case "error", "response.failed":
		msg := g.Get("error.message").String()
		if g.Get("type").String() == "response.failed" {
			msg = g.Get("response.error.message").String()
		}
		a.fail(boundedText(msg))
	}
}

// boundedText bounds an upstream's own words before a client reads them.
func boundedText(msg string) string {
	if msg == "" {
		return "the upstream reported a failure inside its stream"
	}
	if len(msg) > maxResponsesText {
		return strings.ToValidUTF8(msg[:maxResponsesText], "")
	}
	return msg
}

// finish writes the usage and the stop reason, the two things the codec waits
// for, and ends the turn.
func (a *responsesAdapter) finish(g gjson.Result) {
	res := g.Get("response")
	if u := res.Get("usage"); u.Exists() {
		raw, _ := json.Marshal(map[string]int64{"inputTokens": u.Get("input_tokens").Int(), "outputTokens": u.Get("output_tokens").Int()})
		a.emit("metricsEvent", string(raw))
	}
	reason := "end_turn"
	switch {
	case g.Get("type").String() == "response.incomplete" || res.Get("status").String() == "incomplete":
		switch res.Get("incomplete_details.reason").String() {
		case "max_output_tokens", "compaction_required_for_model_handoff":
			reason = "max_tokens"
		}
	case a.calls > 0:
		reason = "tool_use"
	}
	raw, _ := json.Marshal(map[string]string{"stopReason": reason})
	a.emit("metadataEvent", string(raw))
	a.done = true
}
