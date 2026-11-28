// Package metrics keeps the counters behind GET /metrics in OpenMetrics text.
// It records the same dimension tuple the usage rollups group by, so an outside
// dashboard can rebuild the Usage page.
package metrics

import (
	"bufio"
	"io"
	"runtime/metrics"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"rllmx/app/store/analytics"
)

const (
	// maxTuples bounds the series a hostile or unusual client can create. A
	// tuple past the cap keeps its provider and model and files the rest under
	// "other".
	maxTuples = 20000
	// maxPairs bounds the provider and model names themselves, which a client
	// chooses: a pair past the cap is filed as "other" everywhere, so neither
	// the series nor the histograms grow with invented names.
	maxPairs = 2000
)

// Families are the metric names this package writes. The operator guide is
// checked against it, so a family added here cannot go undocumented.
var Families = []string{
	"rllmx_requests", "rllmx_tokens", "rllmx_cost_usd", "rllmx_cost_unknown_requests",
	"rllmx_request_duration_seconds", "rllmx_ttfb_seconds", "rllmx_metrics_series_folded",
	"rllmx_inflight_requests", "rllmx_api_key", "rllmx_account", "rllmx_build",
	"process_start_time_seconds", "go_goroutines", "go_heap_objects_bytes",
}

// buckets are the histogram bounds in seconds; the array keeps the length a
// constant, which the per-series bucket counts need.
var buckets = [...]float64{0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300}

// The tuple every usage series carries: the rollups' grouping.
type dimsKey struct{ provider, model, connection, apiKey, endpoint string }

type reqKey struct {
	dimsKey
	code   string
	cached bool
}

type tokKey struct {
	dimsKey
	kind string
}

type histKey struct{ provider, model string }

type histogram struct {
	counts [len(buckets) + 1]uint64 // the last slot is +Inf
	sum    float64
	count  uint64
}

// Registry holds the counters. Observe is called on the request path; Write is
// called by the scrape.
type Registry struct {
	start time.Time

	mu       sync.Mutex
	seen     map[dimsKey]struct{}
	pairs    map[histKey]struct{}
	folded   uint64
	requests map[reqKey]uint64
	tokens   map[tokKey]uint64
	cost     map[dimsKey]float64
	unknown  map[dimsKey]uint64
	latency  map[histKey]*histogram
	ttfb     map[histKey]*histogram
}

// New returns an empty registry. A nil now takes time.Now.
func New(now func() time.Time) *Registry {
	if now == nil {
		now = time.Now
	}
	return &Registry{
		start:    now(),
		seen:     map[dimsKey]struct{}{},
		pairs:    map[histKey]struct{}{},
		requests: map[reqKey]uint64{},
		tokens:   map[tokKey]uint64{},
		cost:     map[dimsKey]float64{},
		unknown:  map[dimsKey]uint64{},
		latency:  map[histKey]*histogram{},
		ttfb:     map[histKey]*histogram{},
	}
}

// tokenKinds is the order the token counters are written in; a fixed array
// keeps the hot path from allocating a map per request.
var tokenKinds = [...]string{"prompt", "completion", "cached", "cache_write", "reasoning"}

// Observe records one finished request. No field of the record is a body, a
// header or prompt text, so nothing here can carry one out.
func (r *Registry) Observe(rec analytics.Record) {
	if r == nil {
		return
	}
	counts := [len(tokenKinds)]int64{rec.PromptTokens, rec.CompletionTokens, rec.CachedTokens, rec.CacheWriteTokens, rec.ReasoningTokens}
	r.mu.Lock()
	defer r.mu.Unlock()
	name := r.name(rec)
	d := r.tuple(name, rec)
	r.requests[reqKey{d, codeOf(rec.Status), rec.Cached}]++
	for i, n := range counts {
		if n > 0 {
			r.tokens[tokKey{d, tokenKinds[i]}] += uint64(n)
		}
	}
	if rec.CostKnown {
		r.cost[d] += rec.Cost
	} else {
		r.unknown[d]++
	}
	if rec.LatencyMs > 0 {
		r.hist(r.latency, name).observe(float64(rec.LatencyMs) / 1000)
	}
	if rec.TTFBMs > 0 {
		r.hist(r.ttfb, name).observe(float64(rec.TTFBMs) / 1000)
	}
}

// name returns the provider and model a record is counted under, folding a new
// pair past the cap into "other". The caller holds the lock.
func (r *Registry) name(rec analytics.Record) histKey {
	k := histKey{rec.Provider, rec.Model}
	if _, ok := r.pairs[k]; ok {
		return k
	}
	if len(r.pairs) >= maxPairs {
		return histKey{"other", "other"}
	}
	r.pairs[k] = struct{}{}
	return k
}

// tuple returns the record's dimension tuple, folding a new one past the cap.
// The caller holds the lock.
func (r *Registry) tuple(name histKey, rec analytics.Record) dimsKey {
	d := dimsKey{name.provider, name.model, rec.ConnectionID, rec.APIKeyID, rec.Endpoint}
	if _, ok := r.seen[d]; ok {
		return d
	}
	if len(r.seen) >= maxTuples {
		r.folded++
		d = dimsKey{d.provider, d.model, "other", "other", "other"}
	}
	r.seen[d] = struct{}{}
	return d
}

// hist returns the histogram for a pair, creating it. The caller holds the lock.
func (r *Registry) hist(m map[histKey]*histogram, k histKey) *histogram {
	h, ok := m[k]
	if !ok {
		h = &histogram{}
		m[k] = h
	}
	return h
}

func (h *histogram) observe(v float64) {
	h.count++
	h.sum += v
	for i, b := range buckets {
		if v <= b {
			h.counts[i]++
			return
		}
	}
	h.counts[len(buckets)]++
}

// codeOf files a status as the class the dashboard shows.
func codeOf(status int) string {
	if status >= 200 && status <= 599 {
		return strconv.Itoa(status/100) + "xx"
	}
	return "other"
}

// Info is what the scrape reads live: names that would otherwise multiply the
// counters, the in-flight table and the build identity.
type Info struct {
	Version, Commit string
	Keys            []KeyInfo
	Accounts        []AccountInfo
	Inflight        []Active
}

// KeyInfo names one API key. It never carries the key itself.
type KeyInfo struct{ ID, Name, User string }

// AccountInfo names one upstream account.
type AccountInfo struct{ ID, Provider, Name string }

// Active is one provider, model and account with requests in flight.
type Active struct {
	Provider, Model, Connection string
	Count                       int
}

type sample struct {
	labels string
	value  string
}

// Write renders the registry as OpenMetrics 1.0 text. Counters are copied under
// the lock; the write itself holds nothing.
func (r *Registry) Write(w io.Writer, extra Info) error {
	r.mu.Lock()
	folded := r.folded
	requests := make([]sample, 0, len(r.requests))
	for k, v := range r.requests {
		requests = append(requests, sample{labels: k.labels(), value: strconv.FormatUint(v, 10)})
	}
	tokens := make([]sample, 0, len(r.tokens))
	for k, v := range r.tokens {
		tokens = append(tokens, sample{labels: k.labels(), value: strconv.FormatUint(v, 10)})
	}
	cost := make([]sample, 0, len(r.cost))
	for k, v := range r.cost {
		cost = append(cost, sample{labels: k.labels(), value: num(v)})
	}
	unknown := make([]sample, 0, len(r.unknown))
	for k, v := range r.unknown {
		unknown = append(unknown, sample{labels: k.labels(), value: strconv.FormatUint(v, 10)})
	}
	latency := snapshot(r.latency)
	ttfb := snapshot(r.ttfb)
	r.mu.Unlock()

	bw := bufio.NewWriter(w)
	out := &writer{w: bw}
	out.family("rllmx_requests", "counter", "Requests by provider, model, account, key, endpoint, status class and cache.", requests)
	out.family("rllmx_tokens", "counter", "Tokens by provider, model, account, key, endpoint and kind.", tokens)
	out.family("rllmx_cost_usd", "counter", "Spend in US dollars for requests with a known price.", cost)
	out.family("rllmx_cost_unknown_requests", "counter", "Requests whose model has no price.", unknown)
	out.histograms("rllmx_request_duration_seconds", "Request time in seconds, from arrival to the last byte.", latency)
	out.histograms("rllmx_ttfb_seconds", "Time to the first token in seconds.", ttfb)
	out.family("rllmx_metrics_series_folded", "counter", "Tuples folded into \"other\" after the series cap.",
		[]sample{{value: strconv.FormatUint(folded, 10)}})
	out.family("rllmx_inflight_requests", "gauge", "Requests running right now.", inflight(extra.Inflight))
	out.info("rllmx_api_key", "API keys by id, with the name and owning user.", keyInfo(extra.Keys))
	out.info("rllmx_account", "Upstream accounts by connection id, with the account name.", accountInfo(extra.Accounts))
	out.info("rllmx_build", "The running build.", []sample{{labels: pair("version", extra.Version) + pair("commit", extra.Commit), value: "1"}})
	out.family("process_start_time_seconds", "gauge", "Unix time the process started.",
		[]sample{{value: num(float64(r.start.UnixNano()) / 1e9)}})
	out.runtime()
	out.raw("# EOF\n")
	return bw.Flush()
}

// snapshot copies the histograms so the write holds no lock.
func snapshot(m map[histKey]*histogram) map[histKey]histogram {
	out := make(map[histKey]histogram, len(m))
	for k, h := range m {
		out[k] = *h
	}
	return out
}

func inflight(list []Active) []sample {
	out := make([]sample, 0, len(list))
	for _, a := range list {
		out = append(out, sample{
			labels: pair("provider", a.Provider) + pair("model", a.Model) + pair("connection_id", a.Connection),
			value:  strconv.Itoa(a.Count),
		})
	}
	return out
}

func keyInfo(list []KeyInfo) []sample {
	out := make([]sample, 0, len(list))
	for _, k := range list {
		out = append(out, sample{
			labels: pair("api_key_id", k.ID) + pair("api_key_name", k.Name) + pair("user", k.User),
			value:  "1",
		})
	}
	return out
}

func accountInfo(list []AccountInfo) []sample {
	out := make([]sample, 0, len(list))
	for _, a := range list {
		out = append(out, sample{
			labels: pair("connection_id", a.ID) + pair("provider", a.Provider) + pair("account_name", a.Name),
			value:  "1",
		})
	}
	return out
}

func (k dimsKey) labels() string {
	return pair("provider", k.provider) + pair("model", k.model) + pair("connection_id", k.connection) +
		pair("api_key_id", k.apiKey) + pair("endpoint", k.endpoint)
}

func (k reqKey) labels() string {
	return k.dimsKey.labels() + pair("code", k.code) + pair("cached", strconv.FormatBool(k.cached))
}

func (k tokKey) labels() string {
	return k.dimsKey.labels() + pair("kind", k.kind)
}

type writer struct{ w *bufio.Writer }

func (o *writer) raw(s string) { _, _ = o.w.WriteString(s) }

func (o *writer) header(name, typ, help string) {
	o.raw("# HELP " + name + " " + help + "\n")
	o.raw("# TYPE " + name + " " + typ + "\n")
}

// family writes a counter, gauge or info family. A counter's samples carry the
// _total suffix and an info family's the _info suffix, as OpenMetrics requires.
func (o *writer) family(name, typ, help string, samples []sample) {
	o.header(name, typ, help)
	suffix := ""
	switch typ {
	case "counter":
		suffix = "_total"
	case "info":
		suffix = "_info"
	}
	sortSamples(samples)
	for _, s := range samples {
		o.raw(name + suffix + braces(s.labels) + " " + s.value + "\n")
	}
}

func (o *writer) info(name, help string, samples []sample) {
	o.family(name, "info", help, samples)
}

func (o *writer) histograms(name, help string, m map[histKey]histogram) {
	o.header(name, "histogram", help)
	keys := make([]histKey, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b histKey) int {
		return strings.Compare(a.provider+"\x00"+a.model, b.provider+"\x00"+b.model)
	})
	for _, k := range keys {
		h := m[k]
		base := pair("provider", k.provider) + pair("model", k.model)
		var cum uint64
		for i, b := range buckets {
			cum += h.counts[i]
			o.raw(name + "_bucket" + braces(base+pair("le", num(b))) + " " + strconv.FormatUint(cum, 10) + "\n")
		}
		cum += h.counts[len(buckets)]
		o.raw(name + "_bucket" + braces(base+pair("le", "+Inf")) + " " + strconv.FormatUint(cum, 10) + "\n")
		o.raw(name + "_sum" + braces(base) + " " + num(h.sum) + "\n")
		o.raw(name + "_count" + braces(base) + " " + strconv.FormatUint(h.count, 10) + "\n")
	}
}

// runtime writes the two standard gauges from runtime/metrics.
func (o *writer) runtime() {
	o.header("go_goroutines", "gauge", "Goroutines running now.")
	o.header("go_heap_objects_bytes", "gauge", "Bytes of allocated heap objects.")
	samples := []metrics.Sample{{Name: "/sched/goroutines:goroutines"}, {Name: "/memory/classes/heap/objects:bytes"}}
	metrics.Read(samples)
	if samples[0].Value.Kind() == metrics.KindUint64 {
		o.raw("go_goroutines " + strconv.FormatUint(samples[0].Value.Uint64(), 10) + "\n")
	}
	if samples[1].Value.Kind() == metrics.KindUint64 {
		o.raw("go_heap_objects_bytes " + strconv.FormatUint(samples[1].Value.Uint64(), 10) + "\n")
	}
}

func sortSamples(s []sample) {
	sort.Slice(s, func(i, j int) bool { return s[i].labels < s[j].labels })
}

// braces wraps the comma-led label list in the braces a sample needs, or
// returns nothing when the series carries no label.
func braces(labels string) string {
	if labels == "" {
		return ""
	}
	return "{" + strings.TrimPrefix(labels, ",") + "}"
}

// pair renders one label. The value is escaped as OpenMetrics requires.
func pair(name, value string) string {
	if value == "" {
		return ""
	}
	return `,` + name + `="` + labelEscaper.Replace(value) + `"`
}

var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

// num formats a float without an exponent or trailing zeros.
func num(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }

// Sink records a finished request and hands it on to the analytics writer.
type Sink struct {
	Next interface{ Enqueue(analytics.Record) bool }
	Reg  *Registry
}

// Enqueue observes the record, then forwards it. The forward result is the
// sink's own, so a full queue still reads as full. The record is bounded first,
// so a label counted here is one the stored row would also hold.
func (s Sink) Enqueue(r analytics.Record) bool {
	r = analytics.Sanitize(r)
	s.Reg.Observe(r)
	return s.Next.Enqueue(r)
}
