// Package translate converts a request or an answer between the formats a
// client can speak. OpenAI Chat is the hub: every conversion goes source, hub,
// target, so a new format needs one codec and no pair.
package translate

import "encoding/json"

// Role is who a message is from.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// PartType is the kind of one piece of message content.
type PartType string

const (
	PartText  PartType = "text"
	PartImage PartType = "image"
	PartAudio PartType = "audio"
	PartFile  PartType = "file"
)

// Part is one piece of content. An image, audio clip or file is either a URL
// (remote or a data URI) or inline base64 with its media type.
type Part struct {
	Type      PartType
	Text      string
	URL       string
	MediaType string
	Data      string
	Detail    string
	Name      string
}

// ToolCall is a function the model asked to run. Arguments is the JSON text of
// the arguments, exactly as the model produced them.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
	// Custom marks a Responses custom tool, whose single string argument is sent
	// to a chat upstream as the parameter "input".
	Custom bool
	// Signature is a provider's proof for this call (a Gemini thought signature)
	// that must come back with it.
	Signature string
}

// Message is one turn.
type Message struct {
	Role       Role
	Content    []Part
	Name       string
	ToolCalls  []ToolCall
	ToolCallID string
	// Reasoning is the model's visible thinking text, kept apart from Content.
	Reasoning string
	// ReasoningSignature signs Reasoning for providers that check it.
	ReasoningSignature string
	Extra              map[string]json.RawMessage
}

// Tool is a function or custom tool a request offers.
type Tool struct {
	Name        string
	Description string
	// Parameters is the JSON Schema of the arguments.
	Parameters json.RawMessage
	Custom     bool
	// Raw holds a tool the hub has no model for (a hosted tool): it is passed on
	// to a same-format upstream and dropped for any other.
	Raw json.RawMessage
}

// ToolChoice constrains which tool the model may call.
type ToolChoice struct {
	// Mode is auto, none, required or tool.
	Mode string
	Name string
}

// Thinking is the reasoning the request asks for. The zero value asks nothing.
type Thinking struct {
	// Mode is none, auto, level or budget.
	Mode   string
	Level  string
	Budget int
	// Display is how thinking is shown ("summarized", "omitted"), carried only
	// between Claude clients and Claude upstreams.
	Display string
}

// Set reports whether anything was asked.
func (t Thinking) Set() bool { return t.Mode != "" }

// Params are the sampling and limit settings every format shares.
type Params struct {
	MaxTokens        *int
	Temperature      *float64
	TopP             *float64
	TopK             *int
	Stop             []string
	Seed             *int64
	FrequencyPenalty *float64
	PresencePenalty  *float64
	User             string
	ResponseFormat   json.RawMessage
}

// Request is a chat request in the hub.
type Request struct {
	Model      string
	Messages   []Message
	Tools      []Tool
	ToolChoice *ToolChoice
	Params     Params
	Stream     bool
	Thinking   Thinking
	// Extra holds the top-level fields of the source body the hub does not model,
	// so a body that goes out in its own format keeps them.
	Extra map[string]json.RawMessage
}

// Usage is what a request cost in tokens. Prompt includes the cached and
// cache-creation counts, Completion includes Reasoning.
type Usage struct {
	Prompt     int64
	Completion int64
	Cached     int64
	CacheWrite int64
	Reasoning  int64
	// Estimated marks counts the gateway guessed because none were reported.
	Estimated bool
}

// Response is a finished (non-stream) answer. The hub carries the first choice
// only: no client format the router serves asks for more.
type Response struct {
	ID           string
	Model        string
	Created      int64
	Message      Message
	FinishReason string
	Usage        Usage
	Extra        map[string]json.RawMessage
}

// Finish reasons in the hub's vocabulary, which is OpenAI's.
const (
	FinishStop      = "stop"
	FinishLength    = "length"
	FinishToolCalls = "tool_calls"
	FinishFilter    = "content_filter"
)

// ToolCallDelta is one fragment of a streamed tool call. A fragment that opens
// an index carries the id and name; later ones carry argument text only.
type ToolCallDelta struct {
	Index     int
	ID        string
	Name      string
	Arguments string
	// Signature is a Gemini thought signature that rode on the call.
	Signature string
	// Custom marks a Responses custom tool call.
	Custom bool
}

// ChunkError is an error the upstream reported inside a stream.
type ChunkError struct {
	Type    string
	Message string
}

// Chunk is one stream delta in the hub. The codecs' stream readers produce it
// and their stream writers consume it. The first chunk of a stream carries the
// role, at most one carries a finish reason, and usage arrives as a report the
// consumer merges field by field.
type Chunk struct {
	ID        string
	Model     string
	Created   int64
	Role      Role
	Text      string
	Reasoning string
	// Signature is a thinking signature a Claude block closed with.
	Signature    string
	Images       []Part
	ToolCalls    []ToolCallDelta
	FinishReason string
	Usage        *Usage
	Err          *ChunkError
}

// Text joins the text parts of a message.
func (m Message) Text() string {
	var out string
	for _, p := range m.Content {
		if p.Type == PartText {
			out += p.Text
		}
	}
	return out
}

// TextPart is a message body of plain text.
func TextPart(s string) []Part { return []Part{{Type: PartText, Text: s}} }
