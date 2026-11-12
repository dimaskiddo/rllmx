// Package media serves the non-chat endpoints: it resolves the model, picks and
// renews the account, calls the provider's adapter and records the request. It
// is the pipeline's counterpart for a request that is not a conversation.
package media

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/goravel/framework/contracts/log"
	"github.com/tidwall/gjson"

	"rllmx/app/gateway/account"
	"rllmx/app/llm/executor"
	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
	appfailure "rllmx/app/platform/failure"
	appfile "rllmx/app/platform/file"
	"rllmx/app/store/analytics"
	"rllmx/app/store/repository"
)

// Selector picks the next usable account of a provider.
type Selector interface {
	Select(ctx context.Context, req account.Request) (account.Selection, error)
}

// Reporter records how an account did.
type Reporter interface {
	Fail(ctx context.Context, in account.FailInput) (account.Outcome, error)
	Succeed(ctx context.Context, connID, model string, started time.Time) error
}

// Minter makes and renews the credential a provider's login is traded for.
type Minter interface {
	Token(ctx context.Context, conn repository.Connection) (string, error)
	Renew(ctx context.Context, conn repository.Connection, rejected string) (string, error)
}

// Refresher renews a login's token before a request and after a refusal.
type Refresher interface {
	Ready(ctx context.Context, conn repository.Connection) (repository.Connection, error)
	Renew(ctx context.Context, conn repository.Connection) (repository.Connection, error)
}

// Nodes lists the user's provider nodes; a client addresses one by its prefix.
type Nodes interface {
	List(ctx context.Context, typ string) ([]repository.Node, error)
}

// Usage takes the one row a request leaves behind.
type Usage interface {
	Enqueue(r analytics.Record) bool
}

// Adapters is the provider table: which adapter serves a provider and service.
// A provider absent from it has no adapter for that service.
type Adapters interface {
	For(provider, kind string) (media.Adapter, bool)
}

// Deps is what the endpoints need. Only Registry and Adapters are required.
type Deps struct {
	Registry  *upstream.Registry
	Adapters  Adapters
	Client    media.Client
	Selector  Selector
	Reporter  Reporter
	Refresher Refresher
	Minter    Minter
	Nodes     Nodes
	Usage     Usage
	Log       log.Log
	Now       func() time.Time
	// Timeout bounds one media call; zero takes the kind's own bound.
	Timeout time.Duration
}

// Input is one media request.
type Input struct {
	Path      string
	Body      []byte
	RequestID string
	APIKeyID  string
	KeyName   string
	// Binary asks for the image bytes rather than the JSON list.
	Binary bool
}

// Output is the answer, already in the client's shape.
type Output struct {
	Status int
	Header map[string]string
	Body   []byte
}

// Pipeline serves the media endpoints.
type Pipeline struct{ d Deps }

// New builds the endpoints' pipeline.
func New(d Deps) *Pipeline { return &Pipeline{d: d} }

func (p *Pipeline) now() time.Time {
	if p.d.Now != nil {
		return p.d.Now()
	}
	return time.Now()
}

func (p *Pipeline) timeout(kind string, cfg *upstream.MediaConfig) time.Duration {
	if cfg != nil && cfg.TimeoutMs > 0 {
		return time.Duration(cfg.TimeoutMs) * time.Millisecond
	}
	if p.d.Timeout > 0 {
		return p.d.Timeout
	}
	if kind == upstream.KindImage {
		return 150 * time.Second
	}
	return 60 * time.Second
}

// Embeddings serves POST /v1/embeddings.
func (p *Pipeline) Embeddings(ctx context.Context, in Input) Output {
	return p.run(ctx, upstream.KindEmbedding, in)
}

// Images serves POST /v1/images/generations. A caller that asked for bytes gets
// the first image itself.
func (p *Pipeline) Images(ctx context.Context, in Input) Output {
	out := p.run(ctx, upstream.KindImage, in)
	if !in.Binary || out.Status < 200 || out.Status > 299 {
		return out
	}
	if bytes, header, ok := p.binary(ctx, out.Body); ok {
		return Output{Status: out.Status, Header: header, Body: bytes}
	}
	return out
}

// run is the one path both endpoints take: validate, resolve, pick an account,
// call the adapter and record the request once.
func (p *Pipeline) run(ctx context.Context, kind string, in Input) Output {
	row := analytics.Record{
		Time: p.now(), RequestID: in.RequestID, APIKeyID: in.APIKeyID, APIKeyName: in.KeyName,
		Endpoint: in.Path,
	}
	started := p.now()
	defer func() {
		row.LatencyMs = p.now().Sub(started).Milliseconds()
		if p.d.Usage != nil {
			p.d.Usage.Enqueue(row)
		}
	}()

	bad := func(status int, msg string) Output {
		row.Status, row.ErrorClass = status, classOf(status)
		return failureOutput(status, msg)
	}

	var body = in.Body
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return bad(http.StatusBadRequest, "The request body is not valid JSON")
	}
	model := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	if model == "" {
		return bad(http.StatusBadRequest, "The request names no model")
	}
	row.Model = model
	if err := checkBody(kind, body); err != nil {
		return bad(http.StatusBadRequest, strings.ToUpper(err.Error()[:1])+err.Error()[1:])
	}
	ref, ok := p.d.Registry.Media(model, kind)
	entry := upstream.Entry{}
	if !ok {
		// An embedding node is a user endpoint with its own address, never a
		// registry model.
		if kind != upstream.KindEmbedding || !p.nodeRef(ctx, model, &ref, &entry) {
			return bad(http.StatusNotFound, "Model not found")
		}
		ok = true
	}
	if !ok || ref.Kind != kind {
		return bad(http.StatusNotFound, "Model not found")
	}
	row.Provider, row.Model = ref.Provider, ref.Model
	if entry.ID == "" {
		entry, _ = p.d.Registry.ByID(ref.Provider)
	}
	cfg := entry.Media.Config(kind)
	adapter, ok := p.d.Adapters.For(ref.Provider, kind)
	if !ok || cfg == nil {
		return bad(http.StatusBadRequest, "This provider does not serve that model here")
	}

	exclude := map[string]bool{}
	var last *Output
	for {
		conn, out, ok := p.selectAccount(ctx, kind, entry, ref, model, exclude, &row, last)
		if !ok {
			return out
		}
		out, done := p.try(ctx, kind, adapter, entry, cfg, ref, model, conn, body, &row)
		if done {
			return out
		}
		if conn.ID == "" {
			return failureOutput(http.StatusBadGateway, "The provider refused the request")
		}
		exclude[conn.ID] = true
		last = &out
	}
}

// selectAccount picks the next account. A service that takes no credential runs
// without one; every other one walks the provider's accounts.
func (p *Pipeline) selectAccount(ctx context.Context, kind string, entry upstream.Entry, ref upstream.MediaRef, model string, exclude map[string]bool, row *analytics.Record, last *Output) (repository.Connection, Output, bool) {
	cfg := entry.Media.Config(kind)
	noAuth := cfg != nil && (cfg.AuthType == "none" || (entry.Media != nil && entry.Media.NoAuth))
	if p.d.Selector == nil {
		if noAuth {
			return repository.Connection{}, Output{}, true
		}
		return repository.Connection{}, failureOutput(http.StatusNotFound, "No credentials for provider: "+ref.Provider), false
	}
	sel, err := p.d.Selector.Select(ctx, account.Request{Provider: ref.Provider, Model: ref.Model, RequestedModel: model, Exclude: exclude})
	if err != nil {
		// A service that takes no credential still runs without a connection; an
		// existing one only supplies its address.
		if noAuth {
			return repository.Connection{}, Output{}, true
		}
		if errors.Is(err, account.ErrNoConnections) && last != nil {
			return repository.Connection{}, *last, false
		}
		return repository.Connection{}, p.noAccount(ctx, err, ref.Provider, model, row), false
	}
	row.ConnectionID = sel.Conn.ID
	return sel.Conn, Output{}, true
}

// nodeRef resolves a model addressed as <prefix>/<model> to an embedding node,
// the way the chat resolver maps a prefix.
func (p *Pipeline) nodeRef(ctx context.Context, name string, ref *upstream.MediaRef, entry *upstream.Entry) bool {
	prefix, model, ok := strings.Cut(name, "/")
	if !ok || p.d.Nodes == nil {
		return false
	}
	rows, err := p.d.Nodes.List(ctx, "")
	if err != nil {
		return false
	}
	for _, n := range rows {
		if n.Type != upstream.NodeTypeEmbedding && n.Type != upstream.NodeTypeOpenAI {
			continue
		}
		if got, _ := n.Doc().GetString("prefix"); got == "" || got != prefix {
			continue
		}
		base, _ := n.Doc().GetString("baseUrl")
		if base == "" {
			return false
		}
		cfg := &upstream.MediaConfig{BaseURL: executor.NodeURL(n.Type, base, ""), AuthType: "apikey", AuthHeader: "bearer"}
		*entry = upstream.Entry{ID: n.ID, Media: &upstream.Media{ServiceKinds: []string{upstream.KindEmbedding}, Embedding: cfg}}
		*ref = upstream.MediaRef{Provider: n.ID, Kind: upstream.KindEmbedding, Model: model}
		return true
	}
	return false
}

// binary turns an images answer into the image bytes and their headers.
func (p *Pipeline) binary(ctx context.Context, body []byte) ([]byte, map[string]string, bool) {
	first := gjson.GetBytes(body, "data.0")
	b64 := first.Get("b64_json").String()
	if b64 == "" {
		if u := first.Get("url").String(); u != "" {
			fetched, err := media.FetchImage(ctx, p.d.Client, u)
			if err != nil {
				return nil, nil, false
			}
			b64 = fetched
		}
	}
	if b64 == "" {
		return nil, nil, false
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, nil, false
	}
	mime := appfile.ImageMIME(raw)
	if mime == "" {
		return nil, nil, false
	}
	ext := strings.TrimPrefix(mime, "image/")
	if ext == "jpeg" {
		ext = "jpg"
	}
	return raw, map[string]string{"Content-Type": mime, "Content-Disposition": `inline; filename="image.` + ext + `"`}, true
}

func (p *Pipeline) noAccount(ctx context.Context, err error, provider, model string, row *analytics.Record) Output {
	if ctx.Err() != nil {
		row.Status, row.ErrorClass = 499, "client_closed"
		return failureOutput(499, "The client closed the request")
	}
	var locked *account.AllLockedError
	switch {
	case errors.As(err, &locked):
		row.Status, row.ErrorClass = http.StatusServiceUnavailable, "no_account"
		return Output{Status: http.StatusServiceUnavailable, Header: failureHeaders(), Body: appfailure.Body(appfailure.OpenAI, http.StatusServiceUnavailable, "No account is available for "+provider)}
	case errors.Is(err, account.ErrNoConnections):
		row.Status, row.ErrorClass = http.StatusNotFound, "no_account"
		return failureOutput(http.StatusNotFound, "No active credentials for provider: "+provider)
	}
	row.Status, row.ErrorClass = http.StatusInternalServerError, "unknown"
	return failureOutput(http.StatusInternalServerError, "The request could not be served")
}

// try runs the request on one account. done says the answer is final for this
// request; otherwise another account is tried.
func (p *Pipeline) try(ctx context.Context, kind string, adapter media.Adapter, entry upstream.Entry, cfg *upstream.MediaConfig, ref upstream.MediaRef, model string, conn repository.Connection, body []byte, row *analytics.Record) (Output, bool) {
	noAuth := cfg != nil && (cfg.AuthType == "none" || (entry.Media != nil && entry.Media.NoAuth))
	oauthed := conn.ID != "" && conn.AuthType == "oauth" && entry.OAuth != nil
	if oauthed && p.d.Refresher != nil {
		fresh, err := p.d.Refresher.Ready(ctx, conn)
		if err != nil {
			return p.accountFailed(ctx, conn, ref, http.StatusUnauthorized, row, nil, "", noAuth)
		}
		conn = fresh
	}
	mints := oauthed && entry.OAuth.Mint != "" && p.d.Minter != nil
	key := credential(conn, cfg)
	if mints {
		minted, err := p.d.Minter.Token(ctx, conn)
		if err != nil {
			return p.accountFailed(ctx, conn, ref, http.StatusUnauthorized, row, nil, "", noAuth)
		}
		key = minted
	}
	req := media.Request{
		Kind: kind, Provider: ref.Provider, Model: model, WireModel: p.wireModel(entry, cfg, ref.Model),
		Entry: entry, Config: cfg, Credential: key, Account: accountData(conn), Body: body,
	}
	ans, refused, err := p.call(ctx, adapter, req)
	if refused {
		renewed, newKey, ok := p.renew(ctx, conn, entry, cfg, key, mints, row)
		if !ok {
			return p.accountFailed(ctx, conn, ref, http.StatusUnauthorized, row, nil, "", noAuth)
		}
		conn, req.Credential = renewed, newKey
		ans, refused, err = p.call(ctx, adapter, req)
		if refused {
			return p.accountFailed(ctx, conn, ref, http.StatusUnauthorized, row, nil, "", noAuth)
		}
	}
	if err != nil {
		var br *media.BadRequest
		if errors.As(err, &br) {
			row.Status, row.ErrorClass = http.StatusBadRequest, "invalid_request"
			return failureOutput(http.StatusBadRequest, "The request cannot be sent to that provider"), true
		}
		var ne *media.NotEntitled
		if errors.As(err, &ne) {
			// The plan, not the request: pass over the account without reporting.
			row.Status, row.ErrorClass = http.StatusForbidden, "auth"
			return failureOutput(http.StatusForbidden, ne.Reason), false
		}
		var pe *media.ProviderError
		if errors.As(err, &pe) {
			return p.accountFailed(ctx, conn, ref, pe.Status, row, nil, pe.Text, noAuth)
		}
		row.Status, row.ErrorClass = http.StatusBadGateway, "network"
		return failureOutput(http.StatusBadGateway, "The provider could not be reached"), true
	}
	out := Output{Status: ans.Status, Header: ans.Header, Body: ans.Body}
	row.Status = out.Status
	row.ErrorClass = "none"
	if ans.Usage.Known {
		row.PromptTokens, row.CompletionTokens = ans.Usage.Prompt, ans.Usage.Completion
	}
	if !noAuth {
		p.succeed(ctx, conn, ref.Model)
	}
	return out, true
}

// renew replaces a refused credential once: a mint is reminted, a login is
// renewed. ok is false when neither can happen.
func (p *Pipeline) renew(ctx context.Context, conn repository.Connection, entry upstream.Entry, cfg *upstream.MediaConfig, rejected string, mints bool, row *analytics.Record) (repository.Connection, string, bool) {
	if mints {
		key, err := p.d.Minter.Renew(ctx, conn, rejected)
		if err != nil {
			return conn, "", false
		}
		return conn, key, true
	}
	if conn.ID != "" && conn.AuthType == "oauth" && entry.OAuth != nil && p.d.Refresher != nil {
		fresh, err := p.d.Refresher.Renew(ctx, conn)
		if err != nil {
			return conn, "", false
		}
		return fresh, credential(fresh, cfg), true
	}
	return conn, "", false
}

func (p *Pipeline) call(ctx context.Context, adapter media.Adapter, req media.Request) (media.Response, bool, error) {
	if p.d.Client == nil {
		return media.Response{}, false, errors.New("media: no client")
	}
	cctx, cancel := context.WithTimeout(ctx, p.timeout(req.Kind, req.Config))
	defer cancel()
	call, err := adapter.Build(cctx, req, p.d.Client)
	if err != nil {
		return media.Response{}, false, err
	}
	status, header, body, err := p.d.Client.Do(cctx, call.Method, call.URL, call.Header, call.Body)
	if err != nil {
		if cctx.Err() != nil && ctx.Err() == nil {
			return media.Response{}, false, &media.ProviderError{Status: http.StatusGatewayTimeout, Text: "The provider did not answer in time"}
		}
		return media.Response{}, false, err
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		// The adapter never reads a refusal: the gateway renews and retries.
		return media.Response{}, true, nil
	}
	out, err := adapter.Parse(cctx, req, p.d.Client, status, header, body)
	return out, false, err
}

// accountFailed reports the failure and says whether this answer is final. A
// refusal the reporter lets another account serve is not.
func (p *Pipeline) accountFailed(ctx context.Context, conn repository.Connection, ref upstream.MediaRef, status int, row *analytics.Record, out *Output, msg string, noAuth bool) (Output, bool) {
	if noAuth || conn.ID == "" || p.d.Reporter == nil {
		return p.refusalOutput(status, row, out, msg), true
	}
	res, err := p.d.Reporter.Fail(ctx, account.FailInput{
		ConnID: conn.ID, Provider: ref.Provider, Model: ref.Model, Status: status,
	})
	if err == nil && res.Fallback {
		row.Status, row.ErrorClass = clientStatus(status), classOf(status)
		return failureOutput(clientStatus(status), refusalText(msg)), false
	}
	return p.refusalOutput(status, row, out, msg), true
}

// refusalOutput is the client's answer for a failure that ends the request.
func (p *Pipeline) refusalOutput(status int, row *analytics.Record, out *Output, msg string) Output {
	if out != nil {
		return *out
	}
	client := clientStatus(status)
	row.Status, row.ErrorClass = client, classOf(status)
	return failureOutput(client, refusalText(msg))
}

// refusalText is the adapter's fixed word, or the gateway's own.
func refusalText(msg string) string {
	if msg != "" {
		return msg
	}
	return "The provider refused the request"
}

func (p *Pipeline) succeed(ctx context.Context, conn repository.Connection, model string) {
	if conn.ID == "" || p.d.Reporter == nil {
		return
	}
	_ = p.d.Reporter.Succeed(ctx, conn.ID, model, p.now())
}

func failureOutput(status int, msg string) Output {
	return Output{Status: status, Header: failureHeaders(), Body: appfailure.Body(appfailure.OpenAI, status, msg)}
}

func failureHeaders() map[string]string {
	h := appfailure.Headers()
	out := make(map[string]string, len(h))
	for k, v := range h {
		if len(v) > 0 {
			out[k] = v[0]
		}
	}
	return out
}

func classOf(status int) string {
	switch {
	case status == 401 || status == 403:
		return "auth"
	case status == 404:
		return "not_found"
	case status == 429:
		return "rate_limit"
	case status >= 500:
		return "upstream_5xx"
	case status >= 400:
		return "invalid_request"
	}
	return "unknown"
}

func clientStatus(status int) int {
	if status >= 400 && status <= 499 {
		return status
	}
	return http.StatusBadGateway
}

// sizePattern is a width and a height, as OpenAI's sizes and the vendors' own
// aspect strings are written.
var sizePattern = regexp.MustCompile(`^(auto|[0-9]{1,4}x[0-9]{1,4})$`)

// Bounds on what one media request may carry.
const (
	MaxInputs    = 2048
	MaxInputText = 32 << 10
	MaxImages    = 10
)

// checkBody refuses what the endpoint cannot serve before any call.
func checkBody(kind string, body []byte) error {
	switch kind {
	case upstream.KindEmbedding:
		input := gjson.GetBytes(body, "input")
		if !input.Exists() || input.Type == gjson.Null {
			return errors.New("the request names no input")
		}
		switch {
		case input.Type == gjson.String:
			if len(input.String()) > MaxInputText {
				return errors.New("the input is too long")
			}
		case input.IsArray():
			list := input.Array()
			if len(list) > MaxInputs {
				return errors.New("the request carries too many inputs")
			}
			// A string, a list of strings, a token array or a batch of token
			// arrays are all OpenAI's; each is bounded.
			for _, v := range list {
				switch {
				case v.Type == gjson.String:
					if len(v.String()) > MaxInputText {
						return errors.New("an input is too long")
					}
				case v.Type == gjson.Number, v.Type == gjson.True, v.Type == gjson.False:
					// A token id.
				case v.IsArray():
					if len(v.Array()) > MaxInputs {
						return errors.New("an input is too long")
					}
					for _, n := range v.Array() {
						if n.Type != gjson.Number {
							return errors.New("the input is not text or tokens")
						}
					}
				default:
					return errors.New("the input is not text or tokens")
				}
			}
		default:
			return errors.New("the input must be a string or an array")
		}
	case upstream.KindImage:
		if strings.TrimSpace(gjson.GetBytes(body, "prompt").String()) == "" {
			return errors.New("the request names no prompt")
		}
		if n := gjson.GetBytes(body, "n"); n.Exists() && (n.Type != gjson.Number || n.Int() < 1 || n.Int() > MaxImages) {
			return errors.New("the request asks for too many images")
		}
		if size := gjson.GetBytes(body, "size").String(); size != "" && !sizePattern.MatchString(size) {
			return errors.New("the size is not one this router sends")
		}
	}
	return nil
}

// wireModel is the id the provider knows the model by: the model map's path,
// else the entry's upstream id, else the model id.
func (p *Pipeline) wireModel(entry upstream.Entry, cfg *upstream.MediaConfig, model string) string {
	if cfg != nil {
		if t, ok := cfg.ModelMap[model]; ok && t.Path != "" {
			return t.Path
		}
	}
	if p.d.Registry != nil {
		for _, m := range entry.Models {
			if m.ID == model && m.UpstreamID != "" {
				return m.UpstreamID
			}
		}
	}
	return model
}

func credential(conn repository.Connection, cfg *upstream.MediaConfig) string {
	if cfg != nil && cfg.AuthType == "none" {
		return ""
	}
	d := conn.Doc()
	if k, _ := d.GetString("apiKey"); k != "" {
		return k
	}
	k, _ := d.GetString("accessToken")
	return k
}

// accountData is the connection's provider data as short text, for the address
// or the account id an adapter needs.
func accountData(conn repository.Connection) map[string]string {
	out := map[string]string{}
	if project, _ := conn.Doc().GetString("projectId"); project != "" {
		out["projectId"] = project
	}
	psd, _ := conn.Doc().GetRaw("providerSpecificData")
	if psd == "" {
		return out
	}
	gjson.Parse(psd).ForEach(func(k, v gjson.Result) bool {
		if v.Type == gjson.String && len(v.String()) <= 512 {
			out[k.String()] = v.String()
		}
		return true
	})
	return out
}
