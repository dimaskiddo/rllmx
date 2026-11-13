package controllers

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	nethttp "net/http"
	"net/url"
	"strings"
	"time"

	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
	"rllmx/app/platform/file"
	"rllmx/app/platform/network"
)

// Outcome is the closed set of answers a key check can give. Each has one fixed
// message: what the upstream said, which status it used and which address it
// has are never returned, stored or logged.
type Outcome int

const (
	OutcomeValid Outcome = iota
	OutcomeInvalidKey
	OutcomeBlocked
	OutcomeNoConnect
	OutcomeTimedOut
	OutcomeCertificate
	OutcomeRedirected
	OutcomeTooLarge
	OutcomeNotFound
	OutcomeProviderError
	OutcomeUnexpected
	OutcomeUnsupported
	// The three below belong to a login's token, which is renewed and checked
	// but never sent to the provider.
	OutcomeReconnect
	OutcomeRefreshFailed
	OutcomeNoToken
)

var outcomeText = map[Outcome]string{
	OutcomeInvalidKey:    "Invalid API key",
	OutcomeBlocked:       "blocked address",
	OutcomeNoConnect:     "could not connect",
	OutcomeTimedOut:      "timed out",
	OutcomeCertificate:   "certificate not trusted",
	OutcomeRedirected:    "redirected to another address",
	OutcomeTooLarge:      "response too large",
	OutcomeNotFound:      "endpoint not found",
	OutcomeProviderError: "provider error",
	OutcomeUnexpected:    "unexpected response",
	OutcomeUnsupported:   "test not supported yet",
	OutcomeReconnect:     "needs to be reconnected",
	OutcomeRefreshFailed: "token refresh failed",
	OutcomeNoToken:       "no access token",
}

// Message is the fixed text for an outcome; a valid one has none.
func (o Outcome) Message() string { return outcomeText[o] }

// Result is what a check returns.
type Result struct {
	Outcome Outcome
	// Method names the request that decided it: "models", "chat", "messages" or
	// "embeddings".
	Method string
	// Dimensions is the length of an embedding, when one was asked for.
	Dimensions int
	// Warning marks a pass with something worth saying, such as a plan limit.
	Warning bool
}

// Valid reports a passing check.
func (r Result) Valid() bool { return r.Outcome == OutcomeValid }

const (
	checkTimeout   = 15 * time.Second
	checkRedirects = 3
	maxModelList   = 500
	maxModelIDLen  = 256
)

// Checker asks a provider whether a key works and lists the models a node
// serves. It owns one guarded client: every dial is checked, a redirect to
// another host, port or scheme is refused, and the typed key travels only to
// the address it was sent to.
type Checker struct {
	client *nethttp.Client
	cap    int64
	// falURL judges a fal key. fal answers a ping on its queue host with a bare
	// 404 before it reads the key, so only its model catalog distinguishes one.
	falURL string
}

// falModelsURL lists one fal model, the one address that judges a fal key.
const falModelsURL = "https://api.fal.ai/v1/models?limit=1"

// NewChecker builds the checker on the shared guarded client. allow is the
// operator's list of private addresses, or nil.
func NewChecker(allow *network.Allow) *Checker {
	opts := network.Options{ConnectTimeout: checkTimeout, MaxRedirects: checkRedirects, Allow: allow, DisableKeepAlives: true}
	c := network.NewClient(opts)
	c.Timeout = checkTimeout
	inner := c.CheckRedirect
	c.CheckRedirect = func(req *nethttp.Request, via []*nethttp.Request) error {
		if len(via) > 0 && !sameOrigin(via[0].URL, req.URL) {
			return errRedirected
		}
		return inner(req, via)
	}
	return &Checker{client: c, cap: opts.BodyLimit(), falURL: falModelsURL}
}

var errRedirected = errors.New("controllers: redirected to another address")

// sameOrigin reports the same scheme, host and port.
func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Hostname(), b.Hostname()) && port(a) == port(b)
}

func port(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if strings.EqualFold(u.Scheme, "https") {
		return "443"
	}
	return "80"
}

// do sends one request and returns the status and the body, read under the cap.
// The error is already an Outcome: nothing the transport said is kept.
func (c *Checker) do(ctx context.Context, method, target string, header map[string]string, body []byte) (int, []byte, Outcome) {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := nethttp.NewRequestWithContext(ctx, method, target, rd)
	if err != nil {
		return 0, nil, OutcomeNoConnect
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return 0, nil, classifyError(err)
	}
	defer resp.Body.Close()
	data, err := file.ReadCapped(resp.Body, c.cap)
	if errors.Is(err, file.ErrTooLarge) {
		return resp.StatusCode, nil, OutcomeTooLarge
	}
	if err != nil {
		return 0, nil, classifyError(err)
	}
	return resp.StatusCode, data, OutcomeValid
}

// classifyError turns a transport failure into an outcome by its type. The text
// is dropped: it can hold the address and the proxy-style detail of the failure.
func classifyError(err error) Outcome {
	var unknownAuthority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	var netErr net.Error
	switch {
	case errors.Is(err, errRedirected):
		return OutcomeRedirected
	case errors.Is(err, network.ErrBlocked):
		return OutcomeBlocked
	case errors.As(err, &unknownAuthority), errors.As(err, &hostname), errors.As(err, &invalid):
		return OutcomeCertificate
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return OutcomeTimedOut
	}
	return OutcomeNoConnect
}

// judge turns an answer's status into an outcome. bad lists the statuses that
// mean the key was refused.
func judge(status int, bad func(int) bool) Outcome {
	switch {
	case status >= 200 && status < 300:
		return OutcomeValid
	case bad(status):
		return OutcomeInvalidKey
	case status == nethttp.StatusNotFound:
		return OutcomeNotFound
	case status >= 500:
		return OutcomeProviderError
	}
	return OutcomeUnexpected
}

func defaultBad(code int) bool { return code == 401 || code == 403 }

// CheckRegistry asks a registry provider's own validation address. A provider
// with no auth block is one the generic prober cannot judge.
func (c *Checker) CheckRegistry(ctx context.Context, e upstream.Entry, key string) Result {
	if res, ok := c.checkMedia(ctx, e, key); ok {
		return res
	}
	if strings.TrimSpace(key) == "" {
		return Result{Outcome: OutcomeInvalidKey}
	}
	if e.Auth == nil {
		return Result{Outcome: OutcomeUnsupported}
	}
	if p := e.Auth.Probe; p != nil {
		return c.checkProbe(ctx, e, key, p)
	}
	target := e.Auth.WithKey(e.ProbeURL(), key)
	headers := e.Auth.RequestHeaders(key)
	status, _, out := c.do(ctx, nethttp.MethodGet, target, headers, nil)
	if out != OutcomeValid {
		return Result{Outcome: out, Method: "models"}
	}
	return Result{Outcome: judge(status, e.Auth.BadKeyStatus), Method: "models"}
}

// mediaProbeKinds are the services a media-only provider may offer, in the
// order the reference tries their configs.
// mediaProbeKinds follow the reference's order (speech before vectors); search
// and fetch get their own probe with 1.11d.
var mediaProbeKinds = []string{
	upstream.KindTTS, upstream.KindSTT, upstream.KindEmbedding, upstream.KindImage, upstream.KindVideo,
}

// checkMedia tests a media-only provider the way the reference does: one cheap
// request to its own address, where anything but a refused key means the key is
// good. It reports false for a provider that also serves chat.
func (c *Checker) checkMedia(ctx context.Context, e upstream.Entry, key string) (Result, bool) {
	if e.ID == "fal-ai" {
		return c.checkFal(ctx, key), true
	}
	if e.Media == nil || len(e.Media.ServiceKinds) == 0 {
		return Result{}, false
	}
	for _, m := range e.Models {
		if m.Kind == "" || m.Kind == upstream.KindLLM {
			return Result{}, false
		}
	}
	for _, kind := range mediaProbeKinds {
		cfg := e.Media.Config(kind)
		if cfg == nil {
			continue
		}
		if e.Media.NoAuth || cfg.AuthType == "none" {
			return Result{Outcome: OutcomeValid, Method: kind}, true
		}
		if strings.TrimSpace(key) == "" {
			return Result{Outcome: OutcomeInvalidKey, Method: kind}, true
		}
		switch cfg.AuthHeader {
		case "playht", "aws-sigv4":
			// These need provider data the key alone does not carry.
			return Result{Outcome: OutcomeValid, Method: kind, Warning: true}, true
		}
		if cfg.BaseURL == "" || !strings.HasPrefix(cfg.BaseURL, "http") {
			return Result{Outcome: OutcomeUnsupported, Method: kind}, true
		}
		q := url.Values{}
		headers := map[string]string{"Content-Type": "application/json"}
		for k, v := range cfg.Headers {
			headers[k] = v
		}
		media.Auth(cfg, key, headers, q)
		target := cfg.BaseURL
		if len(q) > 0 {
			sep := "?"
			if strings.Contains(target, "?") {
				sep = "&"
			}
			target += sep + q.Encode()
		}
		body := map[string]string{"input": "ping", "text": "ping", "prompt": "ping"}
		if model := probeModel(e, kind, cfg); model != "" {
			body["model"] = model
		}
		raw, err := json.Marshal(body)
		if err != nil {
			return Result{Outcome: OutcomeUnsupported, Method: kind}, true
		}
		status, _, out := c.do(ctx, nethttp.MethodPost, target, headers, raw)
		if out != OutcomeValid {
			return Result{Outcome: out, Method: kind}, true
		}
		// The reference's rule: only a refused key is a bad key, whatever else
		// the provider answers for a ping.
		if status == 401 || status == 403 {
			return Result{Outcome: OutcomeInvalidKey, Method: kind}, true
		}
		return Result{Outcome: OutcomeValid, Method: kind}, true
	}
	return Result{Outcome: OutcomeUnsupported}, true
}

// checkFal asks fal's model catalog whether the key works. Only a refused key
// is a bad key; a catalog that answers anything else means the key was read.
func (c *Checker) checkFal(ctx context.Context, key string) Result {
	if strings.TrimSpace(key) == "" {
		return Result{Outcome: OutcomeInvalidKey, Method: "models"}
	}
	headers := map[string]string{"Authorization": "Key " + key}
	status, _, out := c.do(ctx, nethttp.MethodGet, c.falURL, headers, nil)
	if out != OutcomeValid {
		return Result{Outcome: out, Method: "models"}
	}
	// fal judges a key only by its catalog's own answer: 2xx is a working key, a
	// refusal is a bad one, and anything else says nothing either way.
	return Result{Outcome: judge(status, defaultBad), Method: "models"}
}

// probeModel names the model a media probe sends: the config's default, else
// the first model of that kind.
func probeModel(e upstream.Entry, kind string, cfg *upstream.MediaConfig) string {
	if cfg.DefaultModel != "" {
		return cfg.DefaultModel
	}
	if len(cfg.Models) > 0 {
		return cfg.Models[0].ID
	}
	for _, m := range e.Models {
		if m.Kind == kind {
			return m.ID
		}
	}
	return "test"
}

// checkProbe runs a data file's own key check: one call to the provider's
// origin, judged by its status lists. A soft status passes with a warning.
func (c *Checker) checkProbe(ctx context.Context, e upstream.Entry, key string, p *upstream.Probe) Result {
	method, body := nethttp.MethodGet, []byte(nil)
	if p.Method == "POST" {
		method, body = nethttp.MethodPost, []byte(p.Body)
	}
	headers := e.Auth.RequestHeaders(key)
	status, _, out := c.do(ctx, method, e.ProbeTarget(), headers, body)
	if out != OutcomeValid {
		return Result{Outcome: out, Method: "probe"}
	}
	switch {
	case status >= 200 && status < 300:
		return Result{Outcome: OutcomeValid, Method: "probe"}
	case containsStatus(p.OK, status):
		return Result{Outcome: OutcomeValid, Method: "probe"}
	case containsStatus(p.Soft, status):
		return Result{Outcome: OutcomeValid, Method: "probe", Warning: true}
	case e.Auth.BadKeyStatus(status):
		return Result{Outcome: OutcomeInvalidKey, Method: "probe"}
	}
	return Result{Outcome: judge(status, func(int) bool { return false }), Method: "probe"}
}

// containsStatus reports a status in a list.
func containsStatus(list []int, status int) bool {
	for _, c := range list {
		if c == status {
			return true
		}
	}
	return false
}

// Node is what a node check needs: the stored type and base URL.
type Node struct {
	Type    string
	BaseURL string
}

// CheckNode asks a user-defined node. saved is true for a stored connection,
// which also gets the one-token request an Anthropic node needs to prove a key.
func (c *Checker) CheckNode(ctx context.Context, n Node, key, model string, saved bool) Result {
	if strings.TrimSpace(key) == "" {
		return Result{Outcome: OutcomeInvalidKey}
	}
	base := strings.TrimRight(n.BaseURL, "/")
	if base == "" {
		return Result{Outcome: OutcomeNotFound}
	}
	switch n.Type {
	case upstream.NodeTypeAnthropic:
		return c.checkAnthropic(ctx, base, key, model, saved)
	case upstream.NodeTypeEmbedding:
		return c.checkEmbedding(ctx, base, key, model)
	}
	return c.checkOpenAI(ctx, base, key, model)
}

func bearer(key string) map[string]string { return map[string]string{"Authorization": "Bearer " + key} }

func (c *Checker) checkOpenAI(ctx context.Context, base, key, model string) Result {
	status, _, out := c.do(ctx, nethttp.MethodGet, base+"/models", bearer(key), nil)
	if out != OutcomeValid {
		return Result{Outcome: out, Method: "models"}
	}
	if o := judge(status, defaultBad); o == OutcomeValid || o == OutcomeInvalidKey {
		return Result{Outcome: o, Method: "models"}
	}
	if model == "" {
		return Result{Outcome: judge(status, defaultBad), Method: "models"}
	}
	body, _ := json.Marshal(map[string]any{"model": model, "messages": []map[string]string{{"role": "user", "content": "ping"}}, "max_tokens": 1})
	status, _, out = c.do(ctx, nethttp.MethodPost, base+"/chat/completions", bearer(key), body)
	if out != OutcomeValid {
		return Result{Outcome: out, Method: "chat"}
	}
	return Result{Outcome: judge(status, defaultBad), Method: "chat"}
}

func (c *Checker) checkAnthropic(ctx context.Context, base, key, model string, saved bool) Result {
	h := map[string]string{"x-api-key": key, "anthropic-version": "2023-06-01", "Authorization": "Bearer " + key}
	status, _, out := c.do(ctx, nethttp.MethodGet, base+"/models", h, nil)
	if out != OutcomeValid {
		return Result{Outcome: out, Method: "models"}
	}
	if o := judge(status, defaultBad); o == OutcomeValid || o == OutcomeInvalidKey || !saved {
		return Result{Outcome: o, Method: "models"}
	}
	if model == "" {
		model = "claude-3-haiku-20240307"
	}
	body, _ := json.Marshal(map[string]any{"model": model, "max_tokens": 1, "messages": []map[string]string{{"role": "user", "content": "test"}}})
	status, _, out = c.do(ctx, nethttp.MethodPost, base+"/v1/messages", h, body)
	if out != OutcomeValid {
		return Result{Outcome: out, Method: "messages"}
	}
	// A 400 or 529 still proves the key was accepted; only a refusal means it was not.
	if defaultBad(status) {
		return Result{Outcome: OutcomeInvalidKey, Method: "messages"}
	}
	return Result{Outcome: OutcomeValid, Method: "messages"}
}

func (c *Checker) checkEmbedding(ctx context.Context, base, key, model string) Result {
	status, _, out := c.do(ctx, nethttp.MethodGet, base+"/models", bearer(key), nil)
	if out != OutcomeValid {
		return Result{Outcome: out, Method: "models"}
	}
	if o := judge(status, defaultBad); o == OutcomeValid && model == "" || o == OutcomeInvalidKey {
		return Result{Outcome: o, Method: "models"}
	}
	probe := model
	if probe == "" {
		probe = "test"
	}
	body, _ := json.Marshal(map[string]any{"model": probe, "input": "ping"})
	status, data, out := c.do(ctx, nethttp.MethodPost, base+"/embeddings", bearer(key), body)
	if out != OutcomeValid {
		return Result{Outcome: out, Method: "embeddings"}
	}
	if defaultBad(status) {
		return Result{Outcome: OutcomeInvalidKey, Method: "embeddings"}
	}
	res := Result{Outcome: OutcomeValid, Method: "embeddings"}
	if status >= 200 && status < 300 {
		res.Dimensions = embeddingLength(data)
	}
	return res
}

// embeddingLength reads how long the first embedding is and nothing else: the
// vector itself is never kept.
func embeddingLength(data []byte) int {
	var body struct {
		Data []struct {
			Embedding []json.RawMessage `json:"embedding"`
		} `json:"data"`
	}
	if json.Unmarshal(data, &body) != nil || len(body.Data) == 0 {
		return 0
	}
	return len(body.Data[0].Embedding)
}

// ModelEntry is one model a node reports.
type ModelEntry struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// ListNodeModels fetches a node's own model list. The list is cut to 500
// entries and each id to 256 bytes; an entry with a control character in its id
// or name is dropped.
func (c *Checker) ListNodeModels(ctx context.Context, n Node, key string) ([]ModelEntry, Outcome) {
	if strings.TrimSpace(key) == "" {
		return nil, OutcomeInvalidKey
	}
	base := strings.TrimRight(n.BaseURL, "/")
	if base == "" {
		return nil, OutcomeNotFound
	}
	h := bearer(key)
	if n.Type == upstream.NodeTypeAnthropic {
		h = map[string]string{"x-api-key": key, "anthropic-version": "2023-06-01", "Authorization": "Bearer " + key}
	}
	status, data, out := c.do(ctx, nethttp.MethodGet, base+"/models", h, nil)
	if out != OutcomeValid {
		return nil, out
	}
	if o := judge(status, defaultBad); o != OutcomeValid {
		return nil, o
	}
	return parseModels(data)
}

// parseModels reads the shapes model lists come in: a bare array, or an object
// holding data, models or results.
func parseModels(data []byte) ([]ModelEntry, Outcome) {
	var raw json.RawMessage = data
	var wrap struct {
		Data    json.RawMessage `json:"data"`
		Models  json.RawMessage `json:"models"`
		Results json.RawMessage `json:"results"`
	}
	if json.Unmarshal(data, &wrap) == nil {
		for _, part := range []json.RawMessage{wrap.Data, wrap.Models, wrap.Results} {
			if len(part) > 0 {
				raw = part
				break
			}
		}
	}
	var items []map[string]any
	if json.Unmarshal(raw, &items) != nil {
		return nil, OutcomeUnexpected
	}
	out := make([]ModelEntry, 0, min(len(items), maxModelList))
	for _, it := range items {
		if len(out) == maxModelList {
			break
		}
		id, _ := it["id"].(string)
		name, _ := it["name"].(string)
		if id == "" || len(id) > maxModelIDLen || hasControl(id) || len(name) > maxModelIDLen || hasControl(name) {
			continue
		}
		out = append(out, ModelEntry{ID: id, Name: name})
	}
	return out, OutcomeValid
}
