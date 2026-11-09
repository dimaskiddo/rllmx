// Package upstream holds the provider registry and the price table: what each
// provider is called, where it lives, which models it serves and what they cost.
package upstream

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
)

var (
	categories = map[string]bool{CategoryAPIKey: true, CategoryOAuth: true, CategoryFree: true, CategoryFreeTier: true, CategoryCookie: true}
	formats    = map[string]bool{FormatOpenAI: true, FormatResponses: true, FormatClaude: true, FormatGemini: true}
	executors  = map[string]bool{ExecutorDefault: true, ExecutorAzure: true, ExecutorCodex: true, ExecutorGrokCLI: true, ExecutorGitHub: true}
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
	// ForceStream marks a provider that only answers as a stream, so a client
	// that asked for JSON gets the stream collected into one answer.
	ForceStream bool    `json:"forceStream,omitempty"`
	Models      []Model `json:"models"`
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
}

// Lane is one endpoint of a provider that serves several wire formats: its
// address, the query string and static headers that go with it, and how the
// credential is presented there.
type Lane struct {
	BaseURL   string            `json:"baseUrl"`
	URLSuffix string            `json:"urlSuffix,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Auth      *Auth             `json:"auth,omitempty"`
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
	// Levels are the reasoning efforts the model accepts, when it is stricter
	// than the shared tables.
	Levels []string `json:"levels,omitempty"`
}

// effortLevels are the names a model's levels may use.
var effortLevels = map[string]bool{"none": true, "minimal": true, "low": true, "medium": true, "high": true, "xhigh": true, "max": true, "ultra": true}

// IsEffortLevel reports a level name a request may ask for. none is not one:
// switching thinking off has its own words ("none", "off") in a model suffix.
func IsEffortLevel(name string) bool { return name != "none" && effortLevels[name] }
