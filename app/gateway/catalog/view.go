package catalog

import "rllmx/app/llm/upstream"

// Capabilities is what a client may learn about a model: the public part of
// the capability row. Wire-format switches stay out.
type Capabilities struct {
	Vision      bool `json:"vision"`
	PDF         bool `json:"pdf"`
	AudioInput  bool `json:"audioInput"`
	VideoInput  bool `json:"videoInput"`
	ImageOutput bool `json:"imageOutput"`
	AudioOutput bool `json:"audioOutput"`
	Search      bool `json:"search"`
	Tools       bool `json:"tools"`
	Reasoning   bool `json:"reasoning"`
}

func capabilitiesOf(c upstream.Capability) Capabilities {
	return Capabilities{
		Vision: c.Vision, PDF: c.PDF, AudioInput: c.AudioInput, VideoInput: c.VideoInput,
		ImageOutput: c.ImageOutput, AudioOutput: c.AudioOutput, Search: c.Search, Tools: c.Tools, Reasoning: c.Reasoning,
	}
}

// Entry is one callable model.
type Entry struct {
	// ID is what a client sends: owner, a slash, the model.
	ID       string
	Provider string
	Model    string
	Name     string
	OwnedBy  string
	Caps     upstream.Capability
	// Levels are the reasoning levels a client may ask for; empty for a model
	// with no reasoning.
	Levels []string
}

// OpenAIModel is an entry of GET /v1/models.
type OpenAIModel struct {
	ID                  string       `json:"id"`
	Object              string       `json:"object"`
	Created             int64        `json:"created"`
	OwnedBy             string       `json:"owned_by"`
	Capabilities        Capabilities `json:"capabilities"`
	ContextLength       int          `json:"context_length"`
	MaxCompletionTokens int          `json:"max_completion_tokens"`
	// ThinkingLevels lets a client build its effort selector from the gateway
	// instead of keeping a table per model.
	ThinkingLevels []string `json:"thinking_levels,omitempty"`
}

// OpenAI renders the entry for the OpenAI list.
func (e Entry) OpenAI(created int64) OpenAIModel {
	m := OpenAIModel{
		ID: e.ID, Object: "model", Created: created, OwnedBy: e.OwnedBy, Capabilities: capabilitiesOf(e.Caps),
		ContextLength: e.Caps.ContextWindow, MaxCompletionTokens: e.Caps.MaxOutput,
	}
	if e.Caps.Reasoning && len(e.Levels) > 0 {
		m.ThinkingLevels = e.Levels
	}
	return m
}

// Info is the answer of GET /v1/models/info.
type Info struct {
	ID            string       `json:"id"`
	Name          string       `json:"name"`
	Kind          string       `json:"kind"`
	OwnedBy       string       `json:"owned_by"`
	Endpoint      string       `json:"endpoint"`
	Capabilities  Capabilities `json:"capabilities"`
	ContextWindow int          `json:"contextWindow"`
	MaxOutput     int          `json:"maxOutput"`
}

// Info renders the entry for the info route.
func (e Entry) Info() Info {
	name := e.Name
	if name == "" {
		name = e.Model
	}
	return Info{
		ID: e.ID, Name: name, Kind: kindLLM, OwnedBy: e.OwnedBy, Endpoint: "/v1/chat/completions",
		Capabilities: capabilitiesOf(e.Caps), ContextWindow: e.Caps.ContextWindow, MaxOutput: e.Caps.MaxOutput,
	}
}

// GeminiModel is an entry of GET /v1beta/models.
type GeminiModel struct {
	Name                       string   `json:"name"`
	DisplayName                string   `json:"displayName"`
	Description                string   `json:"description"`
	SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
	InputTokenLimit            int      `json:"inputTokenLimit"`
	OutputTokenLimit           int      `json:"outputTokenLimit"`
}

// Gemini renders the entry for the Gemini list.
func (e Entry) Gemini() GeminiModel {
	name := e.Name
	if name == "" {
		name = e.Model
	}
	return GeminiModel{
		Name: "models/" + e.ID, DisplayName: name, Description: e.OwnedBy + " model " + e.Model,
		SupportedGenerationMethods: []string{"generateContent", "streamGenerateContent"},
		InputTokenLimit:            e.Caps.ContextWindow, OutputTokenLimit: e.Caps.MaxOutput,
	}
}
