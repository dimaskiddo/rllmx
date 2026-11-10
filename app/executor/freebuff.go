package executor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"rllmx/app/upstream"
)

// Freebuff's fixed text and bounds. The marker is the assistant's own name in
// its prompt, not this program's.
const (
	freebuffMarker = "You are Buffy, the strategic coding assistant."
	freebuffCLIUA  = "codebuff-cli/0.0.138"
	// freebuffRunFinish bounds the closing call, which is best effort.
	freebuffRunFinish = 10 * time.Second
)

// freebuffOpenings are the system prompts a session already carries, so the
// marker is not added twice.
var freebuffOpenings = []string{
	"You are Buffy, the strategic coding assistant.",
	"You are Buffy, the Freebuff Cloud project planner.",
	"You are Buffy, a strategic assistant that orchestrates complex coding tasks through specialized sub-agents.",
}

// freebuffAgents are the root agent each free model runs under. The service
// resolves it by name, so the table is data.
var freebuffAgents = map[string]string{
	"deepseek/deepseek-v4-flash": "base3-free-deepseek-flash",
	"deepseek/deepseek-v4-pro":   "base3-free-deepseek",
	"mimo/mimo-v2.5":             "base3-free-mimo",
	"minimax/minimax-m3":         "base3-free-minimax-m3",
	"openai/gpt-5.6-luna":        "base3-free-luna",
}

// freebuffAgent is the agent a model runs under, or the general free one.
func freebuffAgent(model string) string {
	if a, ok := freebuffAgents[model]; ok {
		return a
	}
	return "base2-free"
}

// Freebuff speaks the backend's own envelope: a metadata block at the top level,
// a run registered before the call and finished after it, and a session that
// binds the account to one model.
type Freebuff struct {
	Default *Default
	// Sessions claims the session; nil sends every call without one.
	Sessions SessionSource
}

// SessionSource claims and returns a session for one account and model. A
// session holds the account to one model, so a claim for another is refused.
type SessionSource interface {
	// Claim returns the session's instance id, or "" when the service grants
	// none. fresh skips a cached session.
	Claim(ctx context.Context, token, model string, fresh bool) (string, error)
}

// Execute sends one call: a run is opened, the body is fitted, and the run is
// closed afterwards whatever the answer.
func (f *Freebuff) Execute(ctx context.Context, req Request) (Response, error) {
	if req.Key == "" {
		return Response{}, ErrNoCredential
	}
	instance := ""
	if f.Sessions != nil {
		id, err := f.Sessions.Claim(ctx, req.Key, req.Model, false)
		if err != nil {
			return Response{}, err
		}
		instance = id
	}
	body := freebuffBody(req, instance)
	runID, err := f.run(ctx, req, "START", "", "")
	if err != nil {
		return Response{}, err
	}
	body, serr := sjson.SetBytes(body, "codebuff_metadata.run_id", runID)
	if serr != nil {
		return Response{}, ErrBadAccount
	}
	body, serr = sjson.SetBytes(body, "codebuff_metadata.trace_session_id", newTraceID())
	if serr != nil {
		return Response{}, ErrBadAccount
	}
	req.Body = body
	req.Auth = &upstream.Auth{Scheme: upstream.SchemeBearer, Header: "Authorization", Headers: map[string]string{
		"User-Agent": freebuffCLIUA,
	}}
	resp, err := f.Default.Execute(ctx, req)
	status := "failed"
	if err == nil && resp.Status >= 200 && resp.Status <= 299 {
		status = "completed"
	}
	// The run is closed before the body is handed over: the answer streams, and
	// a request that ends early must not leave a run open.
	f.finish(ctx, req, runID, status)
	return resp, err
}

// freebuffBody fits the request to the backend: reasoning fields dropped, the
// metadata block at the top level, fallbacks off, the assistant's marker in the
// system turn and the closing tool when the request carries tools.
func freebuffBody(req Request, instance string) []byte {
	body := remove(remove(req.Body, "reasoning_effort"), "reasoning")
	body = setValue(body, "provider.allow_fallbacks", false)
	body = setValue(body, "codebuff_metadata.client_id", req.Session)
	body = setValue(body, "codebuff_metadata.cost_mode", "free")
	if instance != "" {
		body = setValue(body, "codebuff_metadata.freebuff_instance_id", instance)
	}
	body = freebuffMarkerIn(body)
	return freebuffEndTurn(body)
}

// freebuffMarkerIn puts the assistant's own opening on the first system turn.
func freebuffMarkerIn(body []byte) []byte {
	msgs := gjson.GetBytes(body, "messages")
	if !msgs.IsArray() || len(msgs.Array()) == 0 {
		return body
	}
	first := msgs.Array()[0]
	if first.Get("role").String() == "system" {
		if cur := first.Get("content"); cur.Type == gjson.String {
			for _, open := range freebuffOpenings {
				if strings.HasPrefix(strings.TrimLeft(cur.String(), " \t\r\n"), open) {
					return body
				}
			}
			out, err := sjson.SetBytes(body, "messages.0.content", freebuffMarker+"\n\n"+cur.String())
			if err != nil {
				return body
			}
			return out
		}
	}
	parts := []string{`{"role":"system","content":` + quoteJSON(freebuffMarker) + `}`}
	for _, m := range msgs.Array() {
		parts = append(parts, m.Raw)
	}
	return setRaw(body, "messages", "["+strings.Join(parts, ",")+"]")
}

// freebuffEndTurn adds the closing tool the backend expects, when the request
// already carries tools. A request with none is left alone.
func freebuffEndTurn(body []byte) []byte {
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() || len(tools.Array()) == 0 {
		return body
	}
	for _, t := range tools.Array() {
		if t.Get("function.name").String() == "end_turn" {
			return body
		}
	}
	parts := make([]string, 0, len(tools.Array())+1)
	for _, t := range tools.Array() {
		parts = append(parts, t.Raw)
	}
	parts = append(parts, `{"type":"function","function":{"name":"end_turn","description":"Signal the end of the current task.","parameters":{"type":"object","properties":{}}}}`)
	return setRaw(body, "tools", "["+strings.Join(parts, ",")+"]")
}

// run opens or closes a run and returns the id the open one named.
func (f *Freebuff) run(ctx context.Context, req Request, action, runID, runStatus string) (string, error) {
	if f.Default == nil || f.Default.Client == nil {
		return "", ErrBadAccount
	}
	var payload string
	if action == "START" {
		payload = `{"action":"START","agentId":` + quoteJSON(freebuffAgent(req.Model)) + `,"ancestorRunIds":[]}`
	} else {
		payload = `{"action":"FINISH","runId":` + quoteJSON(runID) + `,"status":` + quoteJSON(runStatus) + `}`
	}
	call := Request{
		URL: freebuffOrigin(req.URL) + "/api/v1/agent-runs", Method: http.MethodPost, Body: []byte(payload),
		Key: req.Key, Stream: false,
		Auth: &upstream.Auth{Scheme: upstream.SchemeBearer, Header: "Authorization", Headers: map[string]string{"User-Agent": freebuffCLIUA}},
	}
	resp, err := f.Default.once(ctx, call)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.Status == http.StatusUnauthorized {
		return "", ErrBadAccount
	}
	if resp.Status < 200 || resp.Status > 299 {
		return "", ErrUnreachable
	}
	id := gjson.GetBytes(raw, "runId").String()
	if action == "START" && id == "" {
		return "", ErrUnreachable
	}
	return id, nil
}

// finish closes a run, in a goroutine the request does not wait on and that is
// bounded in time. A failed close is not an error: the run ends server-side.
func (f *Freebuff) finish(ctx context.Context, req Request, runID, status string) {
	if runID == "" {
		return
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), freebuffRunFinish)
		defer cancel()
		_, _ = f.run(ctx, req, "FINISH", runID, status)
	}()
	select {
	case <-done:
	case <-time.After(freebuffRunFinish):
	}
}

// freebuffOrigin is the backend's own origin, which serves both the chat and the
// run calls.
func freebuffOrigin(raw string) string {
	if i := strings.Index(raw, "/api/"); i > 0 {
		return raw[:i]
	}
	return strings.TrimSuffix(raw, "/")
}

// newTraceID is one request's trace, made here.
func newTraceID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
