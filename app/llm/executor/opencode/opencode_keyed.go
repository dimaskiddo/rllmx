package opencode

import (
	"context"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"rllmx/app/llm/executor"
	"rllmx/app/llm/upstream"
)

// OpenCodeKeyed serves the gateway's keyed lanes: a bearer key for the paid
// models, and the free identity for the few ids it names.
type OpenCodeKeyed struct {
	Default *executor.Default
	// ResponsesURL is the lane's Responses address; Zen's models named for it go
	// there whatever the client asked.
	ResponsesURL string
	// Zen marks the Zen lane: its own user agent, its own client name, the stub
	// tools its free ids expect, and the free identity for a `-free` id.
	Zen bool
}

// Execute sends one call.
func (o *OpenCodeKeyed) Execute(ctx context.Context, req executor.Request) (executor.Response, error) {
	if req.Key == "" {
		return executor.Response{}, executor.ErrNoCredential
	}
	model := openCodeBaseModel(req.Model)
	if o.Zen && openCodeIsResponses(model) && o.ResponsesURL != "" {
		req.URL = o.ResponsesURL
	}
	if openCodeIsResponses(model) {
		req.Body = openCodeResponsesBody(req.Body, req.Levels)
	}
	if o.Zen {
		req.Body = openCodeZenBody(req.Body, model)
	}
	session := opencodeSessionID("opencode-" + map[bool]string{true: "zen", false: "go"}[o.Zen] + "\x00" + openCodeSeed(req))
	headers := map[string]string{
		"User-Agent":         o.agent(),
		"x-opencode-client":  map[bool]string{true: "desktop", false: "cli"}[o.Zen],
		"x-opencode-project": openCodeProject,
		openCodeSessionHead:  session,
	}
	auth := &upstream.Auth{Scheme: upstream.SchemeBearer, Header: "Authorization", Headers: headers}
	if o.Zen && openCodeFreeModel(model) {
		// A free id is gated behind the free identity even on a keyed account: a
		// real key is refused where this pair succeeds.
		auth.Scheme = ""
		auth.Headers["Authorization"] = openCodePublicKey
		auth.Headers["x-opencode-request"] = opencodeRequestID(o.now())
	}
	req.Auth = auth
	return o.Default.Execute(ctx, req)
}

func (o *OpenCodeKeyed) agent() string {
	if o.Zen {
		return "opencode/1.18.31"
	}
	return ""
}

func (o *OpenCodeKeyed) now() int64 { return time.Now().UnixMilli() }

// openCodeFreeModel reports an id on the free tier: the reference names them by
// the suffix, and the gateway serves them under the free identity.
func openCodeFreeModel(model string) bool { return strings.HasSuffix(model, "-free") }

// openCodeZenBody fits a body to the Zen lane: the stub tools its free ids
// expect on the Chat lane.
func openCodeZenBody(body []byte, model string) []byte {
	if openCodeIsResponses(model) {
		return body
	}
	return openCodeStubTools(body)
}

// openCodeStubTools declares the four file-search tools when the request does
// not already carry one of them. Zen's free ids refuse a request without them.
func openCodeStubTools(body []byte) []byte {
	var out []string
	have := map[string]bool{}
	for _, t := range gjson.GetBytes(body, "tools").Array() {
		if n := openCodeToolName(t); n != "" {
			have[n] = true
		}
		out = append(out, t.Raw)
	}
	for _, name := range []string{"bash", "glob", "grep", "read"} {
		if have[name] {
			continue
		}
		tool := `{"type":"function"}`
		tool, _ = sjson.Set(tool, "function.name", name)
		tool, _ = sjson.Set(tool, "function.description", "OpenCode built-in "+name+" tool")
		tool, _ = sjson.SetRaw(tool, "function.parameters", `{"type":"object","properties":{}}`)
		out = append(out, tool)
	}
	return executor.SetRaw(body, "tools", "["+strings.Join(out, ",")+"]")
}
