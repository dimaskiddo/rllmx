package upstream

import (
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"

	"rllmx/app/platform/text"
)

// Media service kinds a provider's data may name.
const (
	KindLLM       = "llm"
	KindEmbedding = "embedding"
	KindImage     = "image"
	KindTTS       = "tts"
	KindSTT       = "stt"
	KindVideo     = "video"
	KindSearch    = "webSearch"
	KindFetch     = "webFetch"
	KindImageText = "imageToText"
	KindSystem    = "systemone"
)

var mediaKinds = map[string]bool{
	KindLLM: true, KindEmbedding: true, KindImage: true, KindTTS: true, KindSTT: true,
	KindVideo: true, KindSearch: true, KindFetch: true, KindImageText: true, KindSystem: true,
}

var mediaAuthTypes = map[string]bool{"": true, "apikey": true, "oauth": true, "cookie": true, "none": true}

// mediaAuthHeaders are the credential placements a config may name.
var mediaAuthHeaders = map[string]bool{
	"": true, "none": true, "bearer": true, "token": true, "key": true, "key-auth": true,
	"basic": true, "x-api-key": true, "xi-api-key": true, "x-key": true,
	"x-subscription-token": true, "api_key": true, "authorization": true,
	"playht": true, "aws-sigv4": true,
}

const (
	maxMediaModels = 512
	maxModelMap    = 512
)

// Media is the provider's non-chat services. A provider may serve chat as well;
// the configs are what a media request reads, the chat models stay in Models.
type Media struct {
	ServiceKinds []string `json:"serviceKinds,omitempty"`
	// HiddenKinds are served but not offered by the dashboard.
	HiddenKinds []string `json:"hiddenKinds,omitempty"`
	// NoAuth marks a service that takes no credential at all (a local daemon).
	NoAuth bool `json:"noAuth,omitempty"`
	// MediaPriority orders providers offering the same kind.
	MediaPriority int `json:"mediaPriority,omitempty"`
	// CredentialFallback names a provider whose connection this one borrows when
	// it has none of its own: one key serves chat and search (ollama).
	CredentialFallback string `json:"credentialFallback,omitempty"`

	Embedding *MediaConfig `json:"embeddingConfig,omitempty"`
	Image     *MediaConfig `json:"imageConfig,omitempty"`
	TTS       *MediaConfig `json:"ttsConfig,omitempty"`
	STT       *MediaConfig `json:"sttConfig,omitempty"`
	Video     *MediaConfig `json:"videoConfig,omitempty"`
	Search    *MediaConfig `json:"searchConfig,omitempty"`
	Fetch     *MediaConfig `json:"fetchConfig,omitempty"`

	SearchViaChat *SearchViaChat `json:"searchViaChat,omitempty"`
	System        *MediaConfig   `json:"systemoneConfig,omitempty"`
}

// SearchViaChat names the chat model a provider uses for a search it has no
// dedicated endpoint for.
type SearchViaChat struct {
	DefaultModel string `json:"defaultModel,omitempty"`
	Endpoint     string `json:"endpoint,omitempty"`
	PricingURL   string `json:"pricingUrl,omitempty"`
	FreeTier     string `json:"freeTier,omitempty"`
}

// MediaConfig is one service's address, credential shape and model list.
// Voice is one voice a speech service offers.
type Voice struct {
	ID     string `json:"id"`
	Name   string `json:"name,omitempty"`
	Lang   string `json:"lang,omitempty"`
	Gender string `json:"gender,omitempty"`
}

type MediaConfig struct {
	BaseURL string `json:"baseUrl,omitempty"`
	// Path is appended to the base address for a service whose model is a path
	// segment (an embedding model, a TTS voice).
	Path       string `json:"path,omitempty"`
	AuthType   string `json:"authType,omitempty"`
	AuthHeader string `json:"authHeader,omitempty"`
	// Format picks the adapter for a service whose providers share a shape (TTS,
	// STT); the per-provider adapters are chosen by provider id.
	Format       string `json:"format,omitempty"`
	DefaultModel string `json:"defaultModel,omitempty"`
	TimeoutMs    int    `json:"timeoutMs,omitempty"`

	Models []MediaModel `json:"models,omitempty"`
	// Voices are the provider-wide voices; a model's own list wins.
	Voices []Voice `json:"voices,omitempty"`
	// ModelMap maps a client model id to the provider's path, or to a path and a
	// task when the request shape differs.
	ModelMap map[string]ModelTarget `json:"modelMap,omitempty"`

	// Vendor knobs, read by the adapter of that service.
	Headers          map[string]string `json:"headers,omitempty"`
	BodyFields       []string          `json:"bodyFields,omitempty"`
	PollURL          string            `json:"pollUrl,omitempty"`
	ExecutorDriven   bool              `json:"executorDriven,omitempty"`
	Method           string            `json:"method,omitempty"`
	ValidateURL      string            `json:"validateUrl,omitempty"`
	CreditsPerResult float64           `json:"creditsPerResult,omitempty"`
	Formats          []string          `json:"formats,omitempty"`
	SearchTypes      []string          `json:"searchTypes,omitempty"`
	ResponseFormats  map[string]string `json:"responseFormats,omitempty"`
	CostPerQuery     float64           `json:"costPerQuery,omitempty"`
	FreeMonthly      int               `json:"freeMonthlyQuota,omitempty"`
	DefaultResults   int               `json:"defaultMaxResults,omitempty"`
	MaxResults       int               `json:"maxMaxResults,omitempty"`
	CacheTTLMs       int               `json:"cacheTTLMs,omitempty"`
	MaxCharacters    int               `json:"maxCharacters,omitempty"`
}

// MediaModel is one model of one service.
type MediaModel struct {
	ID         string   `json:"id"`
	Name       string   `json:"name,omitempty"`
	UpstreamID string   `json:"upstreamId,omitempty"`
	Dimensions int      `json:"dimensions,omitempty"`
	Params     []string `json:"params,omitempty"`
	Voices     []Voice  `json:"voices,omitempty"`
}

// ModelTarget is a modelMap value: a provider path, or a path with the task
// that changes the request shape (HuggingFace image-to-image).
type ModelTarget struct {
	Path string
	Task string
}

// UnmarshalJSON reads either a bare path or {"path":…, "task":…}.
func (t *ModelTarget) UnmarshalJSON(raw []byte) error {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		t.Path, t.Task = s, ""
		return nil
	}
	var obj struct {
		Path string `json:"path"`
		Task string `json:"task"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return err
	}
	t.Path, t.Task = obj.Path, obj.Task
	return nil
}

// Config returns the service's config, or nil when the provider does not serve
// it. KindLLM has no config.
func (m *Media) Config(kind string) *MediaConfig {
	if m == nil {
		return nil
	}
	switch kind {
	case KindEmbedding:
		return m.Embedding
	case KindImage:
		return m.Image
	case KindTTS:
		return m.TTS
	case KindSTT:
		return m.STT
	case KindVideo:
		return m.Video
	case KindSearch:
		return m.Search
	case KindFetch:
		return m.Fetch
	}
	return nil
}

// Serves reports a declared service kind.
func (m *Media) Serves(kind string) bool {
	return m != nil && len(m.ServiceKinds) > 0 && contains(m.ServiceKinds, kind)
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// validate checks the media block of one entry. chatModels are the entry's own
// chat model ids, used to keep a media id from colliding with one.
func (m *Media) validate(chatModels map[string]bool) error {
	if m == nil {
		return nil
	}
	if len(m.ServiceKinds) == 0 {
		// A file may carry configs for a service it does not offer (the reference
		// has one such entry); nothing is served, so nothing is checked.
		return nil
	}
	seen := map[string]bool{}
	for _, k := range m.ServiceKinds {
		if !mediaKinds[k] {
			return errors.New("unknown media service kind")
		}
		if seen[k] {
			return errors.New("duplicate media service kind")
		}
		seen[k] = true
		if k == KindLLM {
			continue
		}
		if k == KindSystem || k == KindImageText {
			// A structural service: the chat path already answers it.
			continue
		}
		cfg := m.Config(k)
		if k == KindSearch && m.SearchViaChat != nil && (cfg == nil || cfg.BaseURL == "") {
			// A provider that searches through chat serves the kind without an
			// endpoint of its own.
			continue
		}
		if cfg == nil {
			// A provider may declare a service whose adapter resolves the body
			// itself (an executor-driven web provider) or one it never serves.
			continue
		}
		if err := cfg.validate(k, chatModels); err != nil {
			return err
		}
	}
	for _, k := range m.HiddenKinds {
		// A hidden kind may be one the dashboard must not offer even though the
		// provider does not serve it (HuggingFace hides tts it never declares).
		if !mediaKinds[k] {
			return errors.New("a hidden kind is unknown")
		}
	}
	if m.NoAuth {
		changed := false
		for _, k := range m.ServiceKinds {
			if cfg := m.Config(k); cfg != nil && cfg.AuthType != "" && cfg.AuthType != "none" {
				changed = true
			}
		}
		if changed {
			return errors.New("noAuth with a credential config")
		}
	}
	if m.CredentialFallback != "" && !idPattern.MatchString(m.CredentialFallback) {
		return errors.New("a credential fallback has a bad name")
	}
	if m.SearchViaChat != nil {
		sv := m.SearchViaChat
		if len(sv.DefaultModel) > maxNameBytes || text.HasASCIIControl(sv.DefaultModel) ||
			len(sv.PricingURL) > maxNameBytes || text.HasASCIIControl(sv.PricingURL) ||
			len(sv.FreeTier) > 256 || text.HasASCIIControl(sv.FreeTier) {
			return errors.New("a search model has a bad name")
		}
		if sv.Endpoint != "" && !validTemplateURL(sv.Endpoint) {
			return errors.New("a search endpoint is not a URL")
		}
		if sv.PricingURL != "" && !validLinkURL(sv.PricingURL) {
			return errors.New("a search pricing address is not a URL")
		}
	}
	return nil
}

func (c *MediaConfig) validate(kind string, chatModels map[string]bool) error {
	if !mediaAuthTypes[c.AuthType] {
		return errors.New("unknown media auth type")
	}
	if c.BaseURL != "" && !validMediaBase(c.BaseURL, c.AuthType) {
		return errors.New("a media address is not a plain URL")
	}
	if len(c.Format) > 64 || text.HasASCIIControl(c.Format) {
		return errors.New("a media format has a bad name")
	}
	if !mediaAuthHeaders[c.AuthHeader] {
		return errors.New("a media auth header has a bad name")
	}
	if len(c.DefaultModel) > maxNameBytes || text.HasASCIIControl(c.DefaultModel) {
		return errors.New("a media default model has a bad name")
	}
	switch c.Method {
	case "", "GET", "POST":
	default:
		return errors.New("a media method is not GET or POST")
	}
	if c.PollURL != "" && !validMediaBase(c.PollURL, c.AuthType) {
		return errors.New("a media poll address is not a URL")
	}
	if c.CreditsPerResult < 0 {
		return errors.New("a media cost is out of range")
	}
	if len(c.BodyFields) > 32 || len(c.SearchTypes) > 16 || len(c.ResponseFormats) > 16 {
		return errors.New("a media config has too many fields")
	}
	for _, list := range [][]string{c.BodyFields, c.SearchTypes} {
		for _, f := range list {
			if f == "" || len(f) > 64 || text.HasASCIIControl(f) {
				return errors.New("a media field has a bad name")
			}
		}
	}
	for k, v := range c.ResponseFormats {
		if k == "" || len(k) > 32 || len(v) > 64 || text.HasASCIIControl(k) || text.HasASCIIControl(v) {
			return errors.New("a media response format has a bad name")
		}
	}
	if len(c.Path) > maxNameBytes || text.HasASCIIControl(c.Path) {
		return errors.New("a media path has a bad value")
	}
	if c.ValidateURL != "" {
		u, err := url.Parse(c.ValidateURL)
		if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
			return errors.New("a media validate address is not a plain URL")
		}
	}
	if c.TimeoutMs < 0 || c.TimeoutMs > maxTimeoutMs {
		return errors.New("a media timeout is out of range")
	}
	if len(c.Models) > maxMediaModels || len(c.ModelMap) > maxModelMap {
		return errors.New("a media config has too many models")
	}
	seen := map[string]bool{}
	for _, m := range c.Models {
		if m.ID == "" || len(m.ID) > maxNameBytes || text.HasASCIIControl(m.ID) {
			return errors.New("a media model id is empty or too long")
		}
		if seen[m.ID] || chatModels[m.ID] {
			return errors.New("a media model id is duplicated")
		}
		if len(m.Name) > maxNameBytes || text.HasASCIIControl(m.Name) || len(m.UpstreamID) > maxNameBytes || text.HasASCIIControl(m.UpstreamID) {
			return errors.New("a media model has a bad name")
		}
		if m.Dimensions < 0 || m.Dimensions > 1<<20 || len(m.Params) > 32 {
			return errors.New("a media model has a bad dimension")
		}
		for _, p := range m.Params {
			if p == "" || len(p) > 64 || text.HasASCIIControl(p) {
				return errors.New("a media model has a bad parameter")
			}
		}
		seen[m.ID] = true
	}
	for k, v := range c.ModelMap {
		if k == "" || len(k) > maxNameBytes || text.HasASCIIControl(k) || strings.TrimSpace(v.Path) == "" ||
			len(v.Path) > maxNameBytes || text.HasASCIIControl(v.Path) || len(v.Task) > 64 || text.HasASCIIControl(v.Task) {
			return errors.New("a media model map has a bad entry")
		}
	}
	if err := validateVoices(c.Voices); err != nil {
		return err
	}
	for _, m := range c.Models {
		if err := validateVoices(m.Voices); err != nil {
			return err
		}
	}
	for name, value := range c.Headers {
		if !headerNameOK(name) || len(value) > maxNameBytes || text.HasASCIIControl(value) {
			return errors.New("a media header has a bad name or value")
		}
		// A data file supplies headers a request may carry, never a credential
		// or a framing header.
		if ReservedHeader(name) || (c.AuthHeader != "" && strings.EqualFold(name, c.AuthHeader)) {
			return errors.New("a media header names a reserved header")
		}
	}
	if c.CostPerQuery < 0 || c.FreeMonthly < 0 || c.DefaultResults < 0 || c.MaxResults < 0 || c.CacheTTLMs < 0 || c.MaxCharacters < 0 || c.MaxCharacters > 10<<20 ||
		c.DefaultResults > 1000 || c.MaxResults > 1000 || c.CacheTTLMs > 24*60*60*1000 {
		return errors.New("a media search limit is out of range")
	}
	for _, f := range c.Formats {
		if f == "" || len(f) > 32 || text.HasASCIIControl(f) {
			return errors.New("a media format list has a bad entry")
		}
	}
	_ = kind
	return nil
}

// mediaSentinel is a local adapter's marker in place of an address.
var mediaSentinel = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

// maxVoices bounds the voices one provider or model may declare.
const maxVoices = 256

func validateVoices(list []Voice) error {
	if len(list) > maxVoices {
		return errors.New("a media config has too many voices")
	}
	seen := map[string]bool{}
	for _, v := range list {
		if v.ID == "" || len(v.ID) > maxNameBytes || text.HasASCIIControl(v.ID) {
			return errors.New("a voice id is empty or too long")
		}
		if seen[v.ID] {
			return errors.New("a voice id is duplicated")
		}
		seen[v.ID] = true
		for _, f := range []string{v.Name, v.Lang, v.Gender} {
			if len(f) > maxNameBytes || text.HasASCIIControl(f) {
				return errors.New("a voice field is too long")
			}
		}
	}
	return nil
}

// validLinkURL is a display link: http(s) with a host; a fragment is fine.
func validLinkURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil
}

// validTemplateURL is a URL whose only placeholder is the search model.
func validTemplateURL(raw string) bool {
	return validMediaBase(strings.ReplaceAll(raw, "{model}", "model"), "")
}

// validMediaBase accepts a plain http(s) URL, a regional template, or a bare
// marker naming a local adapter for a service that takes no credential.
func validMediaBase(raw, authType string) bool {
	if strings.Contains(raw, "{region}") {
		raw = strings.ReplaceAll(raw, "{region}", "us-east-1")
	}
	u, err := url.Parse(raw)
	if err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil && u.Fragment == "" {
		return true
	}
	return authType == "none" && mediaSentinel.MatchString(raw)
}

// headerNameOK reports a token a request header may carry.
func headerNameOK(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("-_.", c) >= 0) {
			return false
		}
	}
	return true
}
