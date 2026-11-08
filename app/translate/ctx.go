package translate

import "rllmx/app/upstream"

// SignatureStore keeps provider signatures a client may drop between turns
// (Gemini thought signatures). Implementations scope entries per API key.
type SignatureStore interface {
	Get(callID string) (signature string, ok bool)
	Put(callID, signature string)
}

// Ctx is the per-request state the codecs share. A codec may write only the
// fields its doc names.
type Ctx struct {
	ClientFormat string
	TargetFormat string
	Provider     string
	// Model is the upstream model id the body will carry.
	Model string
	// Caps is the capability row for Provider and Model.
	Caps upstream.Capability
	// Entry is the registry entry of the provider; may be zero for a node.
	Entry upstream.Entry
	// Thinking is the request's thinking decision after the name suffix, the
	// client's own fields and the model's defaults were weighed.
	Thinking Thinking
	// CustomTools names the Responses custom tools of the request. The Responses
	// request reader is the only writer.
	CustomTools map[string]bool
	// Calls restores signatures a client dropped. May be nil.
	Calls SignatureStore
	// BudgetOf maps a thinking level to its token budget. May be nil.
	BudgetOf func(level string) int
	// ToolName gives a tool name the upstream returned back to the client. It is
	// set only while the attempt was disguised (see Cloak) and nil otherwise, so
	// a name is never changed on an attempt that renamed nothing.
	ToolName func(name string) string
}

// RestoreTool gives an upstream tool name back to the client.
func (c *Ctx) RestoreTool(name string) string {
	if c == nil || c.ToolName == nil {
		return name
	}
	return c.ToolName(name)
}

// Budget is the token budget of a thinking level for this request's model.
func (c *Ctx) Budget(level string) int {
	if c.BudgetOf == nil {
		return 0
	}
	return c.BudgetOf(level)
}

// NewCtx returns a Ctx for a client format and a target format.
func NewCtx(client, target string) *Ctx {
	return &Ctx{ClientFormat: client, TargetFormat: target, CustomTools: map[string]bool{}}
}
