// Package upstream holds the provider registry and the price table: what each
// provider is called, where it lives, which models it serves and what they cost.
package upstream

import "time"

// Provider categories. The dashboard groups by them; selection ignores them.
const (
	CategoryAPIKey   = "apikey"
	CategoryOAuth    = "oauth"
	CategoryFree     = "free"
	CategoryFreeTier = "freeTier"
	CategoryCookie   = "webCookie"
)

// Wire formats a provider speaks.
const (
	FormatOpenAI    = "openai"
	FormatResponses = "openai-responses"
	FormatClaude    = "claude"
	FormatGemini    = "gemini"
	// FormatCloudCode is a Gemini body inside the Cloud Code envelope (Gemini CLI,
	// Antigravity); FormatVertex is a Gemini body for Vertex AI; FormatKiro is
	// CodeWhisperer's conversation body.
	FormatCloudCode = "gemini-cli"
	FormatVertex    = "vertex"
	FormatKiro      = "kiro"
	// FormatCommandCode is CommandCode's agent wrapper around an Anthropic-shaped
	// conversation; its answer is a stream of AI SDK events.
	FormatCommandCode = "commandcode"
	// FormatOllama is Ollama's own chat body, for the cloud service and a local
	// daemon alike.
	FormatOllama = "ollama"
	// FormatCursor is the OpenAI-shaped body the Cursor agent service is given;
	// its answer is OpenAI chat chunks.
	FormatCursor = "cursor"
)

// ExecutorDefault is the registry-driven executor every provider uses until a
// specialized one is named in the provider's data file.
const ExecutorDefault = "default"

// ExecutorAzure builds its address and headers from the account's own data.
const ExecutorAzure = "azure"

// Executors that shape a body for one backend.
const (
	ExecutorCodex   = "codex"
	ExecutorGrokCLI = "grok-cli"
	ExecutorGitHub  = "github"
	// ExecutorCodeBuddyCN and ExecutorCodeBuddyIntl shape a body for the two
	// CodeBuddy gateways; ExecutorMinimaxCode adds MiniMax Code's per-request headers.
	ExecutorCodeBuddyCN   = "codebuddy-cn"
	ExecutorCodeBuddyIntl = "codebuddy-intl"
	ExecutorMinimaxCode   = "minimax-code"
	// The Google executors: Cloud Code as Gemini CLI or as Antigravity, and
	// Vertex AI with its partner models.
	ExecutorGeminiCLI     = "gemini-cli"
	ExecutorAntigravity   = "antigravity"
	ExecutorVertex        = "vertex"
	ExecutorVertexPartner = "vertex-partner"
	// The AWS executors: Bedrock with an Anthropic body or with a Chat
	// Completions body, and CodeWhisperer's own conversation body.
	ExecutorBedrock    = "bedrock"
	ExecutorBedrockXAI = "bedrock-xai"
	ExecutorKiro       = "kiro"
	// ExecutorOllamaLocal sends to a daemon whose address the account names.
	ExecutorOllamaLocal = "ollama-local"
	// ExecutorXiaomiTokenPlan picks the host of the account's cluster;
	// ExecutorXiaomiMimo adds the account-service route for its own ids.
	ExecutorXiaomiTokenPlan = "xiaomi-tokenplan"
	ExecutorXiaomiMimo      = "xiaomi-mimo"
	// ExecutorCommandCode sends to the agent gateway and turns an error carried
	// inside its stream back into a status.
	ExecutorCommandCode = "commandcode"
	// ExecutorOpenCode is the gateway's free lane; ExecutorOpenCodeGo and
	// ExecutorOpenCodeZen are its keyed lanes.
	ExecutorOpenCode    = "opencode"
	ExecutorOpenCodeGo  = "opencode-go"
	ExecutorOpenCodeZen = "opencode-zen"
	// ExecutorZed exchanges the account's token for a hosted one and speaks the
	// hosted service's own envelope.
	ExecutorZed = "zed"
	// ExecutorFreebuff registers a run, claims a session and speaks the backend's
	// own metadata envelope.
	ExecutorFreebuff = "freebuff"
	// ExecutorQoder encodes and signs a request for the gateway's own protocol.
	ExecutorQoder = "qoder"
	// ExecutorCursor speaks the agent service's Connect-framed protobuf.
	ExecutorCursor = "cursor"
	// ExecutorKimchi strips a body down to what its gateway accepts behind an
	// Anthropic-shaped model.
	ExecutorKimchi = "kimchi"
	// ExecutorDeepseekWeb trades a pasted user token for an access token and
	// guards the completion with a proof of work.
	ExecutorDeepseekWeb = "deepseek-web"
)

var (
	categories = map[string]bool{CategoryAPIKey: true, CategoryOAuth: true, CategoryFree: true, CategoryFreeTier: true, CategoryCookie: true}
	formats    = map[string]bool{FormatOpenAI: true, FormatResponses: true, FormatClaude: true, FormatGemini: true, FormatCloudCode: true, FormatVertex: true, FormatKiro: true, FormatOllama: true, FormatCommandCode: true, FormatCursor: true}
	executors  = map[string]bool{
		ExecutorDefault: true, ExecutorAzure: true, ExecutorCodex: true, ExecutorGrokCLI: true, ExecutorGitHub: true,
		ExecutorCodeBuddyCN: true, ExecutorCodeBuddyIntl: true, ExecutorMinimaxCode: true,
		ExecutorGeminiCLI: true, ExecutorAntigravity: true, ExecutorVertex: true, ExecutorVertexPartner: true,
		ExecutorBedrock: true, ExecutorBedrockXAI: true, ExecutorKiro: true,
		ExecutorOllamaLocal: true, ExecutorXiaomiTokenPlan: true, ExecutorXiaomiMimo: true, ExecutorCommandCode: true,
		ExecutorOpenCode: true, ExecutorOpenCodeGo: true, ExecutorOpenCodeZen: true, ExecutorZed: true, ExecutorFreebuff: true, ExecutorQoder: true,
		ExecutorCursor: true, ExecutorKimchi: true, ExecutorDeepseekWeb: true,
	}
)

// Entry is one provider as its data file describes it. The files are decoded
// strictly, so a field this version does not know fails the load.
type Entry struct {
	ID        string    `json:"id"`
	Alias     string    `json:"alias,omitempty"`
	Aliases   []string  `json:"aliases,omitempty"`
	Category  string    `json:"category"`
	AuthType  string    `json:"authType,omitempty"`
	Display   Display   `json:"display"`
	Transport Transport `json:"transport"`
	Executor  string    `json:"executor,omitempty"`
	// Auth is how a pasted key is checked. Nil means the generic prober cannot
	// check this provider.
	Auth *Auth `json:"auth,omitempty"`
	// OAuth is how an account of this provider is connected by logging in.
	OAuth *OAuth `json:"oauth,omitempty"`
	// NoAuth marks a provider that serves without a credential: a chat request
	// uses a virtual account and the dashboard offers no key form.
	NoAuth bool `json:"noAuth,omitempty"`
	// ForceStream marks a provider that only answers as a stream, so a client
	// that asked for JSON gets the stream collected into one answer.
	ForceStream bool `json:"forceStream,omitempty"`
	// PassthroughModels lists the provider as serving any model id, not only the
	// ones below. An id the list does not name already reaches the executor; the
	// flag is what a catalog or the dashboard reads.
	PassthroughModels bool    `json:"passthroughModels,omitempty"`
	Models            []Model `json:"models"`
	// Media is the provider's non-chat services; nil for a chat-only provider.
	Media *Media `json:"media,omitempty"`
}

// Display is what the dashboard shows.
type Display struct {
	Name string `json:"name"`
}

// Transport is where and how the provider is reached.
type Transport struct {
	// BaseURL is the chat address. It may hold {accountId}, filled from the
	// account's data. Empty only for an executor that builds the address itself.
	BaseURL string `json:"baseUrl"`
	// BaseURLs are the provider's other addresses, tried in the executor's order
	// when the first does not serve a credential. A provider whose executor does
	// not read them has none.
	BaseURLs []string `json:"baseUrls,omitempty"`
	// ResponsesURL and MessagesURL are the addresses of the same provider's
	// Responses and Claude endpoints, used when a model's wire format is one of
	// them. Both stay on the base address's host.
	ResponsesURL string `json:"responsesUrl,omitempty"`
	MessagesURL  string `json:"messagesUrl,omitempty"`
	Format       string `json:"format,omitempty"`
	// Headers are static headers on every request. None is a credential or
	// framing header.
	Headers map[string]string `json:"headers,omitempty"`
	// URLSuffix is a query string added to the address.
	URLSuffix string `json:"urlSuffix,omitempty"`
	// Lanes are the provider's other endpoints, one per wire format. A request
	// whose client format has a lane goes to it with no translation; any other
	// request goes to the provider's own address.
	Lanes map[string]Lane `json:"lanes,omitempty"`
	// Retry replaces the built-in in-place retry rule of the listed statuses.
	Retry map[int]Retry `json:"retry,omitempty"`
	// Quirks are the few body edits a provider needs.
	Quirks Quirks `json:"quirks,omitempty"`
	// ReasoningInject is the scope ("all" or "toolCalls") in which an assistant
	// turn without reasoning text gets a placeholder, for upstreams that refuse
	// a thinking conversation without it.
	ReasoningInject string `json:"reasoningInject,omitempty"`
	// ThinkingFormat is how every model of this provider is asked to think. It wins
	// over a model's own format, for a gateway that reads one shape whatever the
	// model behind it.
	ThinkingFormat string `json:"thinkingFormat,omitempty"`
	// Region is the AWS region an executor builds this provider's address for,
	// when the provider has no single address. A connection may name another.
	Region string `json:"region,omitempty"`
	// SigningKey is the vendor's public key for a provider whose requests are
	// signed with one. It is public data, and it fails the load when it is not a
	// key the signer can use.
	SigningKey string `json:"signingKey,omitempty"`
	// TimeoutMs bounds how long the provider may take to send response
	// headers, over the shared UPSTREAM_CONNECT_TIMEOUT. Zero takes the shared
	// value.
	TimeoutMs int `json:"timeoutMs,omitempty"`
	// StallTimeoutMs bounds how long a stream may stay silent, over the shared
	// STREAM_STALL_TIMEOUT. Zero takes the shared value.
	StallTimeoutMs int `json:"stallTimeoutMs,omitempty"`
	// Regions are the clusters a connection may name, when the provider's host
	// depends on one; DefaultRegion is the one used when it names none.
	Regions       []Region `json:"regions,omitempty"`
	DefaultRegion string   `json:"defaultRegion,omitempty"`
}

// Region is one cluster a provider serves its account from.
type Region struct {
	ID string `json:"id"`
	// Host is the origin a connection in this region reaches.
	Host string `json:"host"`
}

// Timeout is the header bound the provider sets, or zero.
func (t Transport) Timeout() time.Duration {
	if t.TimeoutMs <= 0 {
		return 0
	}
	return time.Duration(t.TimeoutMs) * time.Millisecond
}

// Stall is the silence bound the provider sets, or zero.
func (t Transport) Stall() time.Duration {
	if t.StallTimeoutMs <= 0 {
		return 0
	}
	return time.Duration(t.StallTimeoutMs) * time.Millisecond
}

// RegionHost resolves a region id to its host, falling back to the default.
func (t Transport) RegionHost(id string) string {
	if id == "" {
		id = t.DefaultRegion
	}
	for _, r := range t.Regions {
		if r.ID == id {
			return r.Host
		}
	}
	return ""
}

// Lane is one endpoint of a provider that serves several wire formats: its
// address, the query string and static headers that go with it, and how the
// credential is presented there.
type Lane struct {
	BaseURL   string            `json:"baseUrl"`
	URLSuffix string            `json:"urlSuffix,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Auth      *Auth             `json:"auth,omitempty"`
	// ThinkingFormat is how a model is asked to think on this lane, for a
	// provider whose lanes read different shapes. It beats the provider's own.
	ThinkingFormat string `json:"thinkingFormat,omitempty"`
}

// Retry is an in-place retry rule: Attempts more tries, each after DelayMs.
// A zero delay means the executor's default wait.
type Retry struct {
	Attempts int `json:"attempts"`
	DelayMs  int `json:"delayMs,omitempty"`
}

// Quirks are named body edits. A name this version does not know fails the load.
type Quirks struct {
	// PreserveCacheControl keeps prompt-cache markers on content when a Claude
	// client is served by an OpenAI-format upstream that reads them.
	PreserveCacheControl bool `json:"preserveCacheControl,omitempty"`
	// RequireClaudeToolType writes "custom" as the type of a Claude-format tool
	// that has none, for gateways that reject a typeless tool.
	RequireClaudeToolType bool `json:"requireClaudeToolType,omitempty"`
	// InlineImages fetches remote image URLs and sends the bytes, for an
	// upstream that cannot fetch a URL itself.
	InlineImages bool `json:"inlineImages,omitempty"`
	// ClineEnvelope unwraps a non-stream answer shaped {"success":true,"data":{...}}
	// to its data. Any other body passes unchanged.
	ClineEnvelope bool `json:"clineEnvelope,omitempty"`
	// ReasoningAliasFields copies a string `reasoning` into `reasoning_content` on
	// a passed-through stream delta that carries no `reasoning_content` of its own,
	// for an upstream that streams thinking under the other name.
	ReasoningAliasFields bool `json:"reasoningAliasFields,omitempty"`
}

// Reasoning scopes a provider may name.
const (
	ReasoningAll       = "all"
	ReasoningToolCalls = "toolCalls"
)

// ExecutorNames lists the executors a data file may name.
func ExecutorNames() []string {
	out := make([]string, 0, len(executors))
	for n := range executors {
		out = append(out, n)
	}
	return out
}

// Model is one model a provider serves.
type Model struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	// TargetFormat is the wire format this model is reached in when it differs
	// from the provider's transport format.
	TargetFormat string `json:"targetFormat,omitempty"`
	// UpstreamID is the id the provider's API knows the model by, when it differs
	// from the id clients use.
	UpstreamID string `json:"upstreamId,omitempty"`
	// Lite marks a Codex model that takes its tools and instructions as input
	// items instead of fields.
	Lite bool `json:"lite,omitempty"`
	// DefaultLocation is where a Vertex model is served when the account names no
	// location: "global" for a model that regional locations do not carry.
	DefaultLocation string `json:"defaultLocation,omitempty"`
	// Levels are the reasoning efforts the model accepts, when it is stricter
	// than the shared tables.
	Levels []string `json:"levels,omitempty"`
	// ContextLength is the window the gateway itself truncates to, where it
	// differs from the vendor's: a catalog sync leaves it alone.
	ContextLength int `json:"contextLength,omitempty"`
	// Kind is the service a media model belongs to; empty or "llm" is a chat
	// model. A media model is never offered as a chat model.
	Kind string `json:"kind,omitempty"`
	// Params are the request fields the model accepts (an STT vendor's extras).
	Params []string `json:"params,omitempty"`
	// Dimensions is an embedding's vector length, when the provider fixes one.
	Dimensions int `json:"dimensions,omitempty"`
	// Transport overrides the service's format for one model (an STT vendor).
	Transport string `json:"transport,omitempty"`
	// Voices are the voices a speech model offers.
	Voices []Voice `json:"voices,omitempty"`
}

// effortLevels are the names a model's levels may use.
var effortLevels = map[string]bool{"none": true, "minimal": true, "low": true, "medium": true, "high": true, "xhigh": true, "max": true, "ultra": true}

// IsEffortLevel reports a level name a request may ask for. none is not one:
// switching thinking off has its own words ("none", "off") in a model suffix.
func IsEffortLevel(name string) bool { return name != "none" && effortLevels[name] }
