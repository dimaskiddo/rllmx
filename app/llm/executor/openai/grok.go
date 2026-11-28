package openai

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"rllmx/app/llm/executor"
	"rllmx/app/platform/text"
	"rllmx/app/store/dialect"
	"rllmx/app/store/document"
)

// grokFields are the top-level fields the Grok CLI proxy accepts.
var grokFields = map[string]bool{
	"model": true, "input": true, "instructions": true, "tools": true, "tool_choice": true, "stream": true,
	"store": true, "reasoning": true, "include": true, "temperature": true, "top_p": true,
	"max_output_tokens": true, "parallel_tool_calls": true, "text": true, "metadata": true, "prompt_cache_key": true,
}

// grokNativeID is an item id the proxy itself issued.
var grokNativeID = regexp.MustCompile(`(?i)^(?:rs|msg|fc)_[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

const (
	grokFreeform   = `{"type":"object","properties":{"input":{"type":"string"}},"required":["input"]}`
	grokMaxTurns   = 5000
	grokTurnLife   = 2 * time.Hour
	grokMaxHeader  = 256
	grokDefaultEff = "high"
)

// Grok shapes a Responses body for the Grok CLI proxy and sends the headers the
// official client does. The only state it keeps is the turn counter, bounded
// and private to the executor.
type Grok struct {
	Default *executor.Default
	// Now is the clock; nil uses time.Now.
	Now func() time.Time

	mu    sync.Mutex
	turns map[string]*grokTurn
}

type grokTurn struct {
	turn int
	last time.Time
	// body is the hash of the request that produced turn, so the same request
	// sent again (after a token renewal) keeps its index.
	body string
}

// Execute sends one call.
func (g *Grok) Execute(ctx context.Context, req executor.Request) (executor.Response, error) {
	if req.Key == "" {
		return executor.Response{}, executor.ErrNoCredential
	}
	body := shapeGrok(req)
	req.Body = body
	if req.Auth != nil {
		a := *req.Auth
		a.Headers = g.headers(a.Headers, req, body)
		req.Auth = &a
	}
	return g.Default.Execute(ctx, req)
}

func (g *Grok) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

// headers adds what changes with every request. Static identity headers come
// from the provider's data.
func (g *Grok) headers(base map[string]string, req executor.Request, body []byte) map[string]string {
	h := make(map[string]string, len(base)+9)
	for k, v := range base {
		h[k] = v
	}
	if req.Session != "" {
		h["x-grok-session-id"] = req.Session
		h["x-grok-conv-id"] = req.Session
	}
	h["x-grok-req-id"] = dialect.NewID()
	h["x-grok-turn-idx"] = strconv.Itoa(g.turnIndex(req.Session, body))
	h["x-grok-agent-id"] = grokAgent(req)
	if m := gjson.GetBytes(body, "model").String(); m != "" {
		h["x-grok-model-override"] = m
	}
	for header, field := range map[string]string{"x-email": "email", "x-userid": "userId"} {
		if v := headerSafe(field, req); v != "" {
			h[header] = v
		}
	}
	return h
}

func headerSafe(field string, req executor.Request) string {
	if req.Account == nil {
		return ""
	}
	v := strings.TrimSpace(req.Account(field))
	if v == "" || len(v) > grokMaxHeader || text.HasASCIIControl(v) {
		return ""
	}
	return v
}

// grokAgent is the id the account sends as its agent: the one it was logged in
// with, else one derived from the connection, never from the machine.
func grokAgent(req executor.Request) string {
	for _, f := range []string{"deviceId", "agentId"} {
		if v := headerSafe(f, req); v != "" {
			return v
		}
	}
	sum := sha256.Sum256([]byte("agent:" + req.Account("connectionId")))
	h := hex.EncodeToString(sum[:])
	return h[0:8] + "-" + h[8:12] + "-5" + h[13:16] + "-a" + h[17:20] + "-" + h[20:32]
}

// turnIndex is the 1-based turn of the conversation: the number of user
// messages in the input, never lower than the session's last turn plus one. The
// same request sent again keeps its index.
func (g *Grok) turnIndex(session string, body []byte) int {
	fromInput := countUserTurns(gjson.GetBytes(body, "input"))
	if session == "" {
		return fromInput
	}
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:8])
	now := g.now()
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.turns == nil {
		g.turns = map[string]*grokTurn{}
	}
	prev := g.turns[session]
	if prev != nil && now.Sub(prev.last) > grokTurnLife {
		delete(g.turns, session)
		prev = nil
	}
	if prev != nil && prev.body == hash {
		prev.last = now
		return prev.turn
	}
	turn := fromInput
	if prev != nil {
		turn = max(fromInput, prev.turn+1)
	}
	if prev == nil && len(g.turns) >= grokMaxTurns {
		g.evict(now)
	}
	g.turns[session] = &grokTurn{turn: turn, last: now, body: hash}
	return turn
}

// evict drops expired sessions, and the least recently used one when none had
// expired.
func (g *Grok) evict(now time.Time) {
	oldest, oldestAt := "", now
	for k, t := range g.turns {
		if now.Sub(t.last) > grokTurnLife {
			delete(g.turns, k)
			continue
		}
		if t.last.Before(oldestAt) || oldest == "" {
			oldest, oldestAt = k, t.last
		}
	}
	if len(g.turns) >= grokMaxTurns && oldest != "" {
		delete(g.turns, oldest)
	}
}

func countUserTurns(input gjson.Result) int {
	n := 0
	for _, it := range input.Array() {
		typ := it.Get("type").String()
		if it.Get("role").String() == "user" && (typ == "" || typ == "message") {
			n++
		}
	}
	return max(n, 1)
}

// shapeGrok turns the Responses body the codec wrote into the one the proxy
// accepts.
func shapeGrok(req executor.Request) []byte {
	b := req.Body
	b = executor.SetValue(b, "stream", true)
	b = executor.SetValue(b, "store", false)
	b = grokInput(b)

	id := func(s string) string { return s }
	b, names := flattenTools(b, id, grokFreeform)
	if len(gjson.GetBytes(b, "tools").Array()) == 0 {
		b = executor.Remove(executor.Remove(b, "tools"), "tool_choice")
	} else {
		b = grokToolChoice(b, names)
	}

	b = cacheKey(b, req.Session)
	b = grokReasoning(b, req.Levels)
	return keepOnly(b, grokFields)
}

// grokToolChoice keeps a forced choice only when it names a function the
// request carries or a hosted tool it includes.
func grokToolChoice(b []byte, names map[string]bool) []byte {
	tc := gjson.GetBytes(b, "tool_choice")
	if !tc.IsObject() {
		return b
	}
	typ := tc.Get("type").String()
	if typ == "function" || typ == "custom" {
		n := strings.TrimSpace(tc.Get("name").String())
		if n == "" || !names[cut(n)] {
			return executor.Remove(b, "tool_choice")
		}
		return executor.SetRaw(b, "tool_choice", `{"type":"function","name":`+document.Encode(cut(n))+`}`)
	}
	for _, t := range gjson.GetBytes(b, "tools").Array() {
		if t.Get("type").String() == typ && hostedTools[typ] {
			return b
		}
	}
	return executor.Remove(b, "tool_choice")
}

// grokReasoning sets the summary the CLI asks for, and the effort only for a
// model that declares levels: the others refuse the field.
func grokReasoning(b []byte, levels []string) []byte {
	effort := gjson.GetBytes(b, "reasoning.effort").String()
	b = executor.SetValue(b, "reasoning.summary", "concise")
	if len(levels) == 0 {
		b = executor.Remove(b, "reasoning.effort")
		effort = ""
	} else {
		switch {
		case effort == "max":
			effort = "xhigh"
		case !slices.Contains([]string{"low", "medium", "high", "xhigh"}, effort):
			effort = grokDefaultEff
		}
		b = executor.SetValue(b, "reasoning.effort", effort)
	}
	if effort == "none" {
		return b
	}
	return executor.SetRaw(b, "include", `["reasoning.encrypted_content"]`)
}

// grokInput normalizes the conversation items: freeform tool calls become
// function calls, a reasoning item is kept only when the proxy issued it, and a
// tool output with no call is dropped.
func grokInput(b []byte) []byte {
	in := gjson.GetBytes(b, "input")
	if !in.IsArray() {
		return b
	}
	type shaped struct {
		raw, typ, callID string
	}
	list := in.Array()
	items := make([]shaped, 0, len(list))
	calls := map[string]bool{}
	for _, it := range list {
		out, ok := grokItem(it)
		if !ok {
			continue
		}
		raw := gjson.Parse(out)
		s := shaped{raw: out, typ: raw.Get("type").String(), callID: raw.Get("call_id").String()}
		if s.typ == "function_call" {
			calls[s.callID] = true
		}
		items = append(items, s)
	}
	kept := make([]string, 0, len(items))
	for _, s := range items {
		if s.typ == "function_call_output" && !calls[s.callID] {
			continue
		}
		kept = append(kept, s.raw)
	}
	if len(kept) == 0 {
		return b
	}
	return executor.SetRaw(b, "input", "["+strings.Join(kept, ",")+"]")
}

func grokItem(it gjson.Result) (string, bool) {
	if !it.IsObject() {
		return "", false
	}
	raw := it.Raw
	raw, _ = sjson.Delete(raw, "internal_chat_message_metadata_passthrough")
	callID := document.FirstString(it.Get("call_id"), it.Get("id"))
	switch it.Get("type").String() {
	case "reasoning":
		if !grokNativeID.MatchString(it.Get("id").String()) || it.Get("encrypted_content").Type != gjson.String {
			return "", false
		}
	case "custom_tool_call":
		name := strings.TrimSpace(it.Get("name").String())
		if callID == "" || name == "" {
			return "", false
		}
		args, _ := sjson.Set("{}", "input", toolText(firstRaw(it.Get("input"), it.Get("arguments"))))
		out, _ := sjson.Set(`{"type":"function_call"}`, "call_id", callID)
		out, _ = sjson.Set(out, "name", name)
		out, _ = sjson.Set(out, "arguments", args)
		return out, true
	case "function_call_output", "custom_tool_call_output":
		if callID == "" {
			return "", false
		}
		out, _ := sjson.Set(`{"type":"function_call_output"}`, "call_id", callID)
		out, _ = sjson.Set(out, "output", toolText(it.Get("output")))
		return out, true
	case "function_call":
		name := strings.TrimSpace(it.Get("name").String())
		if callID == "" || name == "" {
			return "", false
		}
		out, _ := sjson.Set(`{"type":"function_call"}`, "call_id", callID)
		if grokNativeID.MatchString(it.Get("id").String()) {
			out, _ = sjson.Set(out, "id", it.Get("id").String())
		}
		out, _ = sjson.Set(out, "name", name)
		args := it.Get("arguments")
		if args.Type == gjson.String {
			out, _ = sjson.Set(out, "arguments", args.String())
		} else {
			out, _ = sjson.Set(out, "arguments", orEmpty(args.Raw))
		}
		if st := it.Get("status").String(); st != "" {
			out, _ = sjson.Set(out, "status", st)
		}
		return out, true
	case "item_reference":
		return "", false
	}
	if id := it.Get("id").String(); id != "" && !grokNativeID.MatchString(id) && serverID.MatchString(id) {
		raw, _ = sjson.Delete(raw, "id")
	}
	return raw, true
}

var serverID = regexp.MustCompile(`^(rs|fc|resp|msg)_`)

func orEmpty(raw string) string {
	if raw == "" {
		return "{}"
	}
	return raw
}

func firstRaw(vals ...gjson.Result) gjson.Result {
	for _, v := range vals {
		if v.Exists() {
			return v
		}
	}
	return gjson.Result{}
}

// toolText is a tool argument or output as the text the proxy takes.
func toolText(v gjson.Result) string {
	if !v.Exists() {
		return ""
	}
	if v.Type == gjson.String {
		return v.String()
	}
	return v.Raw
}
