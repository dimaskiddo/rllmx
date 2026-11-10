package commandcode

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/translate"
)

// The CommandCode workspace fields. The reference reports the directory it
// happens to run in, the host's platform and the host's user; this router names
// fixed ones instead, so nothing about the machine leaves it.
const (
	commandCodeDefaultMaxTokens = 32000
	commandCodeTemperature      = 0.3
	commandCodeWorkingDir       = "/workspace"
	commandCodeEnvironment      = "linux"
)

// Codec joins CommandCode's agent body to the hub. Its conversation
// is Anthropic-shaped inside a wrapper, and its answer is a stream of AI SDK
// events.
type Codec struct{}

func init() { translate.Register(Codec{}) }

// Name is the format name.
func (Codec) Name() string { return translate.FormatCommandCode }

// ReadOnly reports that no client speaks this format.
func (Codec) ReadOnly() bool { return true }

// ReadRequest refuses a conversation body: the format is an upstream's, and no
// route serves a client in it.
func (Codec) ReadRequest(body []byte, c *translate.Ctx) (*translate.Request, error) {
	return nil, translate.ErrBadBody
}

// WriteResponse refuses to answer in this format: no client speaks it.
func (Codec) WriteResponse(r *translate.Response, c *translate.Ctx) ([]byte, error) {
	return nil, translate.ErrBadBody
}

// ReadResponse reads a whole answer, for a caller that asked for one. The
// upstream streams, so this is the defensive path: a JSON body with a message.
func (Codec) ReadResponse(body []byte, c *translate.Ctx) (*translate.Response, error) {
	if !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
		return nil, translate.ErrBadBody
	}
	g := gjson.ParseBytes(body)
	out := &translate.Response{ID: g.Get("id").String(), Model: g.Get("model").String(), Created: g.Get("created").Int()}
	out.Message.Role = translate.RoleAssistant
	out.Message.Content = []translate.Part{{Type: translate.PartText, Text: g.Get("content").String()}}
	out.FinishReason = translate.FinishStop
	return out, nil
}

// WriteRequest builds the wrapper the upstream reads: a thread id, a workspace
// description and the conversation under params.
func (c Codec) WriteRequest(r *translate.Request, ctx *translate.Ctx) ([]byte, error) {
	params := translate.Obj{
		"model":    r.Model,
		"stream":   true,
		"messages": commandCodeMessages(r.Messages),
	}
	max := int64(commandCodeDefaultMaxTokens)
	if r.Params.MaxTokens != nil {
		max = int64(*r.Params.MaxTokens)
	}
	params["max_tokens"] = max
	params["temperature"] = commandCodeTemperature
	if r.Params.Temperature != nil {
		params["temperature"] = *r.Params.Temperature
	}
	if r.Params.TopP != nil {
		params["top_p"] = *r.Params.TopP
	}
	if system := commandCodeSystem(r.Messages); system != "" {
		params["system"] = system
	}
	if tools := commandCodeTools(r.Tools); tools != nil {
		params["tools"] = tools
	}
	body := translate.Obj{
		"threadId": commandCodeThread(r, ctx),
		"memory":   "",
		"config": translate.Obj{
			"workingDir": commandCodeWorkingDir, "date": time.Now().UTC().Format("2006-01-02"),
			"environment": commandCodeEnvironment, "structure": []any{},
			"isGitRepo": false, "currentBranch": "", "mainBranch": "", "gitStatus": "", "recentCommits": []any{},
		},
		"params": params,
	}
	return json.Marshal(body)
}

// commandCodeThread is the conversation's id, made UUID-shaped from the keyed
// session the pipeline derived, so a conversation keeps one thread across
// restarts and no client picks another's.
func commandCodeThread(r *translate.Request, c *translate.Ctx) string {
	seed := r.Model
	if c != nil && c.Session != "" {
		seed = c.Session
	}
	sum := sha256.Sum256([]byte(seed))
	h := hex.EncodeToString(sum[:])
	variant := (h[16]&0x3 | 0x8)
	return h[:8] + "-" + h[8:12] + "-4" + h[13:16] + "-" + string("89ab"[variant&0x3]) + h[17:20] + "-" + h[20:32]
}

// commandCodeSystem joins the system turns; the upstream takes one string and
// refuses a system message inside the list.
func commandCodeSystem(msgs []translate.Message) string {
	var parts []string
	for _, m := range msgs {
		if m.Role == translate.RoleSystem {
			if t := strings.TrimSpace(m.Text()); t != "" {
				parts = append(parts, t)
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

// commandCodeMessages maps the turns. Every content is an array of blocks, a
// tool result is its own role, and an assistant turn that thought or called a
// tool opens with a reasoning block because the upstream expects one.
func commandCodeMessages(msgs []translate.Message) []any {
	out := make([]any, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case translate.RoleSystem:
			continue
		case translate.RoleTool:
			out = append(out, translate.Obj{
				"role": "tool",
				"content": []any{translate.Obj{
					"type": "tool-result", "toolCallId": m.ToolCallID, "toolName": m.Name,
					"output": translate.Obj{"type": "text", "value": m.Text()},
				}},
			})
		case translate.RoleAssistant:
			blocks := []any{}
			reasoning := m.Reasoning
			if reasoning == "" && len(m.ToolCalls) > 0 {
				reasoning = " "
			}
			if reasoning != "" {
				blocks = append(blocks, translate.Obj{"type": "reasoning", "text": reasoning})
			}
			if t := m.Text(); t != "" {
				blocks = append(blocks, translate.Obj{"type": "text", "text": t})
			}
			for _, tc := range m.ToolCalls {
				blocks = append(blocks, translate.Obj{
					"type": "tool-call", "toolCallId": tc.ID, "toolName": tc.Name,
					"input": commandCodeInput(tc.Arguments),
				})
			}
			if len(blocks) == 0 {
				blocks = append(blocks, translate.Obj{"type": "text", "text": ""})
			}
			out = append(out, translate.Obj{"role": "assistant", "content": blocks})
		default:
			out = append(out, translate.Obj{"role": "user", "content": commandCodeBlocks(m.Content)})
		}
	}
	return out
}

// commandCodeBlocks maps one turn's parts. Only an image carried inline can be
// sent: the upstream takes the bytes, not a link.
func commandCodeBlocks(parts []translate.Part) []any {
	out := make([]any, 0, len(parts))
	for _, p := range parts {
		switch p.Type {
		case translate.PartText:
			out = append(out, translate.Obj{"type": "text", "text": p.Text})
		case translate.PartImage:
			if uri := inlineImage(p); uri != "" {
				out = append(out, translate.Obj{"type": "image", "image": uri, "mimeType": imageMedia(p), "mediaType": imageMedia(p)})
			}
		}
	}
	if len(out) == 0 {
		out = append(out, translate.Obj{"type": "text", "text": ""})
	}
	return out
}

// inlineImage is the data URI of an image part, or "" when it has none.
func inlineImage(p translate.Part) string {
	if p.Data != "" {
		return "data:" + imageMedia(p) + ";base64," + p.Data
	}
	if strings.HasPrefix(p.URL, "data:") {
		return p.URL
	}
	return ""
}

func imageMedia(p translate.Part) string {
	if p.MediaType != "" {
		return p.MediaType
	}
	if p.URL != "" && strings.HasPrefix(p.URL, "data:") {
		head, _, _ := strings.Cut(strings.TrimPrefix(p.URL, "data:"), ";")
		if head != "" && strings.Contains(head, "/") {
			return head
		}
	}
	return "image/png"
}

// commandCodeInput is a tool call's arguments as the object the upstream reads.
func commandCodeInput(args string) any {
	if strings.TrimSpace(args) == "" {
		return translate.Obj{}
	}
	var v any
	if json.Unmarshal([]byte(args), &v) == nil {
		return v
	}
	return translate.Obj{}
}

// commandCodeTools maps the tool specs to the plain Anthropic shape.
func commandCodeTools(tools []translate.Tool) []any {
	if len(tools) == 0 {
		return nil
	}
	out := make([]any, 0, len(tools))
	for _, t := range tools {
		schema := any(translate.Obj{"type": "object"})
		if len(t.Parameters) > 0 {
			schema = json.RawMessage(t.Parameters)
		}
		out = append(out, translate.Obj{"name": t.Name, "description": t.Description, "input_schema": schema})
	}
	return out
}
