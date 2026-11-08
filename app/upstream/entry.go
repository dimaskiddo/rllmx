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

var (
	categories = map[string]bool{CategoryAPIKey: true, CategoryOAuth: true, CategoryFree: true, CategoryFreeTier: true, CategoryCookie: true}
	formats    = map[string]bool{FormatOpenAI: true, FormatResponses: true, FormatClaude: true, FormatGemini: true}
	executors  = map[string]bool{ExecutorDefault: true}
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
	BaseURL string `json:"baseUrl"`
	Format  string `json:"format,omitempty"`
}

// Model is one model a provider serves.
type Model struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	// TargetFormat is the wire format this model is reached in when it differs
	// from the provider's transport format.
	TargetFormat string `json:"targetFormat,omitempty"`
}
