package gemini

import (
	"encoding/json"
	"regexp"
	"rllmx/app/llm/translate"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Names the Cloud Code envelope carries for the two clients that use it.
const (
	UserAgentGeminiCLI   = "gemini-cli"
	UserAgentAntigravity = "antigravity"
)

// CloudCodeCodec is a Gemini body inside the Cloud Code envelope that Gemini CLI
// and Antigravity send: {project, model, userAgent, requestId, request}. It
// writes the Gemini body with the Gemini codec and wraps it; an answer is read
// with the Gemini codec after the wrapper is taken off. Whose envelope it is
// comes from the provider: Antigravity's has no safety settings and its own
// placeholder signature.
type CloudCodeCodec struct{ GeminiCodec }

func init() { translate.Register(CloudCodeCodec{}) }

// Name is the format name.
func (CloudCodeCodec) Name() string { return translate.FormatCloudCode }

// isAntigravity reports the Antigravity variant of the envelope.
func isAntigravity(c *translate.Ctx) bool { return c.Provider == "antigravity" }

// WriteRequest writes the Gemini body for the model and wraps it. The project and
// the request id are left for the executor to fill, because they belong to the
// account and the body is built once for every account.
func (g CloudCodeCodec) WriteRequest(r *translate.Request, c *translate.Ctx) ([]byte, error) {
	cc := *c
	if isAntigravity(c) {
		cc.Safety = false
	} else {
		cc.Safety = true
	}
	body, err := g.GeminiCodec.WriteRequest(r, &cc)
	if err != nil {
		return nil, err
	}
	if isAntigravity(c) {
		body = antigravityBody(body, r.Model)
	}
	agent := UserAgentGeminiCLI
	if isAntigravity(c) {
		agent = UserAgentAntigravity
	}
	env := translate.Obj{"project": "", "model": r.Model, "userAgent": agent, "requestId": "", "request": json.RawMessage(body)}
	return json.Marshal(env)
}

// ReadResponse reads an answer, which may or may not still be wrapped.
func (g CloudCodeCodec) ReadResponse(body []byte, c *translate.Ctx) (*translate.Response, error) {
	return g.GeminiCodec.ReadResponse(unwrapCloudCode(body), c)
}

// unwrapCloudCode takes the {"response": ...} wrapper off an answer.
func unwrapCloudCode(body []byte) []byte {
	if !translate.ValidObject(body) {
		return body
	}
	if w := gjson.GetBytes(body, "response"); w.IsObject() && !gjson.GetBytes(body, "candidates").Exists() {
		return []byte(w.Raw)
	}
	return body
}

// antigravityBody applies the rules Antigravity's backend needs on the request
// part of the envelope: tools in one group with the validated mode, thinking
// fields the backend rejects removed, branding rewritten in the system text and
// the output ceiling held to what it serves.
func antigravityBody(body []byte, model string) []byte {
	if !gjson.ValidBytes(body) {
		return body
	}
	for _, k := range antigravityBlacklist {
		body, _ = sjson.DeleteBytes(body, k)
	}
	body, _ = sjson.DeleteBytes(body, "safetySettings")
	if parts := gjson.GetBytes(body, "systemInstruction.parts"); parts.IsArray() {
		for i, p := range parts.Array() {
			if txt := p.Get("text"); txt.Type == gjson.String {
				body, _ = sjson.SetBytes(body, "systemInstruction.parts."+strconv.Itoa(i)+".text", RewriteBranding(txt.String()))
			}
		}
	}
	if n := gjson.GetBytes(body, "generationConfig.maxOutputTokens"); n.Int() > antigravityMaxOutput {
		body, _ = sjson.SetBytes(body, "generationConfig.maxOutputTokens", antigravityMaxOutput)
	}
	if gjson.GetBytes(body, "tools.0.functionDeclarations.0").Exists() {
		body, _ = sjson.SetBytes(body, "toolConfig.functionCallingConfig.mode", "VALIDATED")
	}
	return body
}

// antigravityMaxOutput is the most output tokens Antigravity serves per request.
const antigravityMaxOutput = 64000

// antigravityBlacklist are the fields of another vendor's thinking dialect that
// Google's generateContent refuses.
var antigravityBlacklist = []string{"output_config", "thinking", "reasoning_effort", "reasoning", "enable_thinking", "thinking_budget", "thinkingConfig"}

var (
	// brandingAgentSDK is the line Claude's agent SDK puts first in a system
	// prompt; Antigravity answers a prompt that carries it with a quota error.
	brandingAgentSDK = "You are a Claude agent, built on Anthropic's Claude Agent SDK."
	brandingHermes   = regexp.MustCompile(`(?i)You are Hermes(?: Agent)?(?:,\s*(?:an intelligent AI assistant|an AI assistant|an AI agent))?(?:,?\s*(?:built|created)\s+by\s+Nous Research)?\.`)
	brandingBilling  = regexp.MustCompile(`(?im)^x-anthropic-billing-header:[^\n]*(?:\r?\n)*`)
	brandingOpenCode = regexp.MustCompile(`(?i)opencode`)
)

// RewriteBranding removes the names of competing clients from a system prompt.
// Antigravity's backend flags a request that carries them and answers a quota
// error, whatever the quota is.
func RewriteBranding(s string) string {
	s = strings.ReplaceAll(s, brandingAgentSDK, "")
	s = brandingHermes.ReplaceAllString(s, "You are an AI assistant.")
	s = brandingBilling.ReplaceAllString(s, "")
	return brandingOpenCode.ReplaceAllStringFunc(s, func(m string) string {
		switch m {
		case "OpenCode":
			return "Antigravity"
		case "OPENCODE":
			return "ANTIGRAVITY"
		}
		return "antigravity"
	})
}
