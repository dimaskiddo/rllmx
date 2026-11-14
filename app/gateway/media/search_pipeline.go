package media

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"golang.org/x/text/unicode/norm"

	"rllmx/app/llm/upstream"
	appfailure "rllmx/app/platform/failure"
	"rllmx/app/platform/network"
	apptext "rllmx/app/platform/text"
)

const (
	// searchBudget is the reference's overall allowance for one search.
	searchBudget = 15 * time.Second
	maxQuery     = 2000
	maxFetchURL  = 2048
	maxFilters   = 50
)

// Search serves POST /v1/search. The provider is the model: the body names it,
// so there is no model id to resolve.
func (p *Pipeline) Search(ctx context.Context, in Input) Output {
	ctx, cancel := context.WithTimeout(ctx, searchBudget)
	defer cancel()
	return p.service(ctx, upstream.KindSearch, in)
}

// Fetch serves POST /v1/web/fetch: a URL, read by a vendor or by the built-in
// converter.
func (p *Pipeline) Fetch(ctx context.Context, in Input) Output {
	return p.service(ctx, upstream.KindFetch, in)
}

// service is the path a search or fetch takes: resolve the provider named in the
// body, fit the request to it, then the same account loop every media call uses.
func (p *Pipeline) service(ctx context.Context, kind string, in Input) Output {
	row, done := p.record(in)
	defer done()
	bad := func(status int, msg string) Output {
		row.Status, row.ErrorClass = status, appfailure.ClassOf(status)
		return failureOutput(status, msg)
	}

	if len(in.Body) == 0 || !gjson.ValidBytes(in.Body) {
		return bad(http.StatusBadRequest, "The request body is not valid JSON")
	}
	body := gjson.ParseBytes(in.Body)
	name := strings.TrimSpace(body.Get("provider").String())
	if name == "" {
		name = strings.TrimSpace(body.Get("model").String())
	}
	if name == "" {
		return bad(http.StatusBadRequest, "The request names no provider")
	}
	id, ok := p.d.Registry.Resolve(name)
	if !ok {
		return bad(http.StatusBadRequest, "Unknown provider: "+name)
	}
	entry, _ := p.d.Registry.ByID(id)
	if !servesKind(entry.Media, kind) {
		return bad(http.StatusBadRequest, "That provider does not serve this service")
	}
	cfg := entry.Media.Config(kind)
	if cfg == nil {
		// A provider that searches through chat has no endpoint of its own; the
		// adapter reads the model from the entry.
		cfg = &upstream.MediaConfig{}
	}
	normalized, msg := p.fit(ctx, kind, body, cfg)
	if msg != "" {
		return bad(http.StatusBadRequest, msg)
	}
	if kind == upstream.KindSearch && id == "searxng" {
		if p.d.SearxngURL == "" {
			return bad(http.StatusBadRequest, "SearXNG is not configured")
		}
		copied := *cfg
		copied.BaseURL = p.d.SearxngURL
		cfg = &copied
	}

	row.Provider, row.Model = id, id
	ref := upstream.MediaRef{Provider: id, Kind: kind}
	return p.serve(ctx, kind, in, ref, entry, cfg, parsed{model: id, body: normalized}, kindPrefix(kind)+id, row)
}

// fit builds the canonical body an adapter reads, refusing what the provider
// cannot serve. A non-empty message is the client's answer.
func (p *Pipeline) fit(ctx context.Context, kind string, body gjson.Result, cfg *upstream.MediaConfig) ([]byte, string) {
	if kind == upstream.KindSearch {
		return searchBody(body, cfg)
	}
	return p.fetchBody(ctx, body, cfg)
}

// searchBody normalizes a query and its options.
func searchBody(body gjson.Result, cfg *upstream.MediaConfig) ([]byte, string) {
	query, ok := cleanQuery(body.Get("query").String())
	if !ok {
		return nil, "The query is empty or not text"
	}
	types := cfg.SearchTypes
	searchType := body.Get("search_type").String()
	if searchType == "" && len(types) > 0 {
		searchType = types[0]
	}
	if searchType == "" {
		searchType = "web"
	}
	if len(types) > 0 && !hasText(types, searchType) {
		return nil, "That search type is not served here"
	}
	for _, field := range []string{"country", "language", "time_range"} {
		if v := body.Get(field); v.Exists() && v.Type != gjson.String {
			return nil, "A search option is not text"
		}
		if v := body.Get(field).String(); len(v) > 32 || apptext.HasASCIIControl(v) {
			return nil, "A search option is too long"
		}
	}
	offset := 0
	if v := body.Get("offset"); v.Exists() {
		if v.Type != gjson.Number || v.Int() < 0 || v.Int() > 10000 {
			return nil, "The offset is not a number this router sends"
		}
		offset = int(v.Int())
	}
	var filters []string
	if v := body.Get("domain_filter"); v.Exists() {
		if !v.IsArray() || len(v.Array()) > maxFilters {
			return nil, "The domain filter is too long"
		}
		for _, item := range v.Array() {
			if item.Type != gjson.String || item.String() == "" || len(item.String()) > 253 || apptext.HasASCIIControl(item.String()) {
				return nil, "A domain filter entry is not a host"
			}
			filters = append(filters, item.String())
		}
	}
	out := searchRequest{
		Query: query, MaxResults: maxResults(body, cfg), SearchType: searchType,
		Country: body.Get("country").String(), Language: body.Get("language").String(),
		TimeRange: body.Get("time_range").String(), Offset: offset, DomainFilter: filters,
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return nil, "The request cannot be built"
	}
	return raw, ""
}

// fetchBody normalizes a fetch request and refuses a target that is not public
// before any provider is asked to read it.
func (p *Pipeline) fetchBody(ctx context.Context, body gjson.Result, cfg *upstream.MediaConfig) ([]byte, string) {
	target := strings.TrimSpace(body.Get("url").String())
	if target == "" || len(target) > maxFetchURL || apptext.HasASCIIControl(target) {
		return nil, "The request names no address"
	}
	if err := network.CheckPublic(ctx, target, nil); err != nil {
		return nil, "The address is not one a fetch may reach"
	}
	format := strings.ToLower(strings.TrimSpace(body.Get("format").String()))
	if format == "" {
		format = "markdown"
	}
	if len(cfg.Formats) > 0 && !hasText(cfg.Formats, format) {
		return nil, "That format is not served here"
	}
	limit := cfg.MaxCharacters
	if n := body.Get("max_characters"); n.Exists() {
		if n.Type != gjson.Number || n.Int() < 1 {
			return nil, "The character limit is not a number this router sends"
		}
		if limit == 0 || int(n.Int()) < limit {
			limit = int(n.Int())
		}
	}
	if limit == 0 || limit > 1<<20 {
		limit = 1 << 20
	}
	raw, err := json.Marshal(fetchRequest{URL: target, Format: format, MaxCharacters: limit})
	if err != nil {
		return nil, "The request cannot be built"
	}
	return raw, ""
}

// maxResults is the count a search asks for: the body's, else the provider's
// default, under its own cap.
func maxResults(body gjson.Result, cfg *upstream.MediaConfig) int {
	n := cfg.DefaultResults
	if n == 0 {
		n = 5
	}
	if v := body.Get("max_results"); v.Exists() && v.Type == gjson.Number {
		if asked := int(v.Int()); asked > 0 {
			n = asked
		}
	}
	if limit := cfg.MaxResults; limit > 0 && n > limit {
		n = limit
	}
	return n
}

// cleanQuery is the query as the reference sends it: no control character, NFKC,
// one space between words. A tab or a newline is whitespace there, not a control.
func cleanQuery(raw string) (string, bool) {
	for i := 0; i < len(raw); i++ {
		if c := raw[i]; (c < 0x20 && c != '\t' && c != '\n') || c == 0x7f {
			return "", false
		}
	}
	q := strings.Join(strings.Fields(norm.NFKC.String(raw)), " ")
	if q == "" || len(q) > maxQuery {
		return "", false
	}
	return q, true
}

// servesKind reports a provider that serves the service, through its own
// endpoint or, for search, through chat.
func servesKind(m *upstream.Media, kind string) bool {
	if m == nil {
		return false
	}
	if kind == upstream.KindSearch {
		return m.Search != nil || m.SearchViaChat != nil
	}
	return m.Config(kind) != nil
}

// hasText reports a list that holds want.
func hasText(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// kindPrefix scopes a lock to the capability, so a failed search never takes the
// same account offline for chat. It is the reference's own lock scope.
func kindPrefix(kind string) string {
	if kind == upstream.KindFetch {
		return "webfetch:"
	}
	return "websearch:"
}

// stampTiming fills the metrics a search or fetch answer carries about the call
// that produced it. The adapter cannot know when the request started.
func stampTiming(out Output, ms int64) Output {
	if out.Status < 200 || out.Status > 299 || len(out.Body) == 0 {
		return out
	}
	body, err := sjson.SetBytes(out.Body, "metrics.response_time_ms", ms)
	if err != nil {
		return out
	}
	if body, err = sjson.SetBytes(body, "metrics.upstream_latency_ms", ms); err != nil {
		return out
	}
	out.Body = body
	return out
}

// searchRequest is the canonical body a search adapter reads.
type searchRequest struct {
	Query        string   `json:"query"`
	MaxResults   int      `json:"max_results"`
	SearchType   string   `json:"search_type"`
	Country      string   `json:"country,omitempty"`
	Language     string   `json:"language,omitempty"`
	TimeRange    string   `json:"time_range,omitempty"`
	Offset       int      `json:"offset,omitempty"`
	DomainFilter []string `json:"domain_filter,omitempty"`
}

// fetchRequest is the canonical body a fetch adapter reads.
type fetchRequest struct {
	URL           string `json:"url"`
	Format        string `json:"format"`
	MaxCharacters int    `json:"max_characters"`
}
