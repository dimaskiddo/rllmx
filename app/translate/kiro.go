package translate

import (
	"encoding/json"
	"strings"
)

// Kiro's thinking trigger. A model that does not take the native effort fields
// is asked to think with these tags in its first user turn.
const (
	kiroThinkingDefault = 16000
	kiroThinkingMax     = 32000
	kiroAgenticSuffix   = "-agentic"
	kiroThinkingSuffix  = "-thinking"
)

// kiroAgenticPrompt keeps a model's writes inside the upstream's timeout. It is
// the reference's own text, sent only for a model whose id asks for it.
const kiroAgenticPrompt = `# CRITICAL: CHUNKED WRITE PROTOCOL (MANDATORY)

You MUST follow these rules for ALL file operations. Violation causes server timeouts and task failure.

## ABSOLUTE LIMITS
- **MAXIMUM 350 LINES** per single write/edit operation - NO EXCEPTIONS
- **RECOMMENDED 300 LINES** or less for optimal performance
- **NEVER** write entire files in one operation if >300 lines

## MANDATORY CHUNKED WRITE STRATEGY

### For NEW FILES (>300 lines total):
1. FIRST: Write initial chunk (first 250-300 lines)
2. THEN: Append remaining content in chunks
3. REPEAT: Continue appending until complete`

// KiroCodec joins CodeWhisperer's conversation body to the hub.
type KiroCodec struct{}

func init() { Register(KiroCodec{}) }

// Name is the format name.
func (KiroCodec) Name() string { return FormatKiro }

// ReadOnly reports that no client speaks this format.
func (KiroCodec) ReadOnly() bool { return true }

// ReadRequest refuses a conversation body: this format is an upstream's, and a
// client's is named by the route it called, so no endpoint accepts one.
func (KiroCodec) ReadRequest(body []byte, c *Ctx) (*Request, error) {
	return nil, ErrBadBody
}

// WriteRequest builds the conversation body from the hub. The system text, the
// thinking trigger and the current time ride on the first user turn, because
// the upstream refuses a top-level system field.
func (k KiroCodec) WriteRequest(r *Request, c *Ctx) ([]byte, error) {
	model, agentic, thinking := kiroIntent(r.Model)
	specs, names := kiroSpecs(r.Tools)
	turns := kiroTurns(r)
	prefix := kiroPrefix(r, c, thinking, agentic)
	turns = replayKiro(turns, c, prefix)
	canon := canonicalizeKiro(turns, specs, names)
	if !canon.Valid {
		return nil, ErrBadBody
	}
	msg := canon.Current
	msg.Tools = specs
	msg.ModelID = model
	payload := map[string]any{
		"conversationState": map[string]any{
			"chatTriggerType": kiroChatTrigger,
			"conversationId":  c.session(),
			"currentMessage":  map[string]any{"userInputMessage": kiroWireUser(msg)},
			"history":         kiroWireHistory(canon.History, model),
		},
	}
	if fields := kiroEffort(r, model); fields != nil {
		payload["additionalModelRequestFields"] = fields
	}
	if cfg := kiroInference(r); cfg != nil {
		payload["inferenceConfig"] = cfg
	}
	return json.Marshal(payload)
}

// kiroChatTrigger is the only trigger value the upstream accepts.
const kiroChatTrigger = "MANUAL"

// kiroIntent reads what a model id asks for beyond the alias: whether to send
// the agentic prompt and whether to ask for thinking.
func kiroIntent(model string) (alias string, agentic, thinking bool) {
	m := strings.TrimSpace(model)
	agentic = strings.HasSuffix(m, kiroAgenticSuffix)
	m = strings.TrimSuffix(m, kiroAgenticSuffix)
	thinking = strings.HasSuffix(m, kiroThinkingSuffix)
	m = strings.TrimSuffix(m, kiroThinkingSuffix)
	return kiroAlias(m), agentic, thinking
}

// ReadResponse refuses a whole answer: the upstream answers with events only,
// so a JSON body from it is not something this format can read.
func (KiroCodec) ReadResponse(body []byte, c *Ctx) (*Response, error) {
	return nil, ErrBadBody
}

// WriteResponse refuses to answer in this format: no client speaks it.
func (KiroCodec) WriteResponse(r *Response, c *Ctx) ([]byte, error) {
	return nil, ErrBadBody
}
