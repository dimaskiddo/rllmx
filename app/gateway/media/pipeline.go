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

	"rllmx/app/access/identity"
	"rllmx/app/gateway/account"
	"rllmx/app/llm/executor"
	"rllmx/app/llm/media"
	"rllmx/app/llm/oauth"
	"rllmx/app/llm/upstream"
	appfailure "rllmx/app/platform/failure"
	appfile "rllmx/app/platform/file"
	"rllmx/app/platform/network"
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

// Connections reads one connection by id, for a video poll that names the
// account which created the job.
type Connections interface {
	Get(ctx context.Context, id string) (repository.Connection, bool, error)
}

// Credentials stores a credential a provider rotated during a call.
type Credentials interface {
	UpdateApiKey(ctx context.Context, connID, key string) error
}

// VoiceLister asks a provider for its own voice list.
type VoiceLister func(ctx context.Context, provider string, r media.Request, c media.Client) ([]upstream.Voice, error)

// Usage takes the one row a request leaves behind.
type Usage interface {
	Enqueue(r analytics.Record) bool
}

// Adapters is the provider table: which adapter serves a provider and service.
// A provider absent from it has no adapter for that service; a transport marker
// may pick a second one (gemini-live).
type Adapters interface {
	For(provider, kind, transport string) (media.Handler, bool)
}

// Deps is what the endpoints need. Only Registry and Adapters are required.
type Deps struct {
	Registry *upstream.Registry
	Adapters Adapters
	Client   media.Client
	// FetchClient reads a fetch target directly, for the built-in converter. It
	// follows redirects, each hop through the same guard. Nil reuses Client.
	FetchClient media.Client
	Selector    Selector
	Reporter    Reporter
	Refresher   Refresher
	Minter      Minter
	Nodes       Nodes
	// Connections looks up the account a video poll names.
	Connections Connections
	// Credentials stores a credential the provider rotated during the call.
	Credentials Credentials
	Voices      VoiceLister
	Usage       Usage
	Log         log.Log
	Now         func() time.Time
	// Network is the guarded dialer's configuration, for an adapter that opens a
	// socket of its own.
	Network network.Options
	// VideoTimeout bounds one video call; zero takes the reference's default.
	VideoTimeout time.Duration
	// SearxngURL is the instance a searxng search is sent to; empty serves none.
	// The address is the operator's, never a client's.
	SearxngURL string
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
	// Form is the parsed multipart of a transcription.
	Form *Form
	// ResponseFormat is the query's answer-format hint, which wins over the
	// body's own.
	ResponseFormat string
	// ContentType is the body's own media type, which a video create forwards.
	ContentType string
	// IdempotencyKey is the client's key for a billable video create.
	IdempotencyKey string
	// ConnectionID pins a request to one account: the one a video job belongs
	// to.
	ConnectionID string
	// Caller is who the request acts as; its allow list decides what a restricted
	// key may call.
	Caller identity.Caller
	// Provider is the query's provider hint for a video poll.
	Provider string
}

// Form is a transcription upload: one file and its fields.
type Form struct {
	Audio       []byte
	Filename    string
	ContentType string
	Fields      map[string]string
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
	switch kind {
	case upstream.KindImage:
		return 150 * time.Second
	case upstream.KindTTS:
		return 120 * time.Second
	case upstream.KindSTT:
		// The reference's route budget, enough for a polling transcription.
		return 300 * time.Second
	case upstream.KindVideo:
		// One HTTP round trip, not the job's own rendering time.
		if p.d.VideoTimeout > 0 {
			return p.d.VideoTimeout
		}
		return 120 * time.Second
	case upstream.KindSearch:
		return searchBudget
	case upstream.KindFetch:
		return 30 * time.Second
	}
	return 60 * time.Second
}

// Embeddings serves POST /v1/embeddings.
func (p *Pipeline) Embeddings(ctx context.Context, in Input) Output {
	parsed, out, ok := parseJSON(in.Body)
	if !ok {
		return p.reject(in, out)
	}
	return p.run(ctx, upstream.KindEmbedding, in, parsed)
}

// Images serves POST /v1/images/generations. A caller that asked for bytes gets
// the first image itself.
func (p *Pipeline) Images(ctx context.Context, in Input) Output {
	parsed, bad, ok := parseJSON(in.Body)
	if !ok {
		return p.reject(in, bad)
	}
	out := p.run(ctx, upstream.KindImage, in, parsed)
	if !in.Binary || out.Status < 200 || out.Status > 299 {
		return out
	}
	if bytes, header, ok := p.binary(ctx, out.Body); ok {
		return Output{Status: out.Status, Header: header, Body: bytes}
	}
	return out
}

// parsed is one request's model and payload, whichever route it came from.
type parsed struct {
	model          string
	body           []byte
	upload         *media.Upload
	modelTransport string
	// action is the video create the request names; empty for every other
	// service.
	action string
}

// parseJSON reads the model of a JSON body, refusing a body that is not one.
func parseJSON(body []byte) (parsed, Output, bool) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return parsed{}, failureOutput(http.StatusBadRequest, "The request body is not valid JSON"), false
	}
	model := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	if model == "" {
		return parsed{}, failureOutput(http.StatusBadRequest, "The request names no model"), false
	}
	return parsed{model: model, body: body}, Output{}, true
}

// run is the one path every endpoint takes: validate, resolve, pick an account,
// call the adapter and record the request once.
func (p *Pipeline) run(ctx context.Context, kind string, in Input, req parsed) Output {
	row, done := p.record(in)
	defer done()

	bad := func(status int, msg string) Output {
		row.Status, row.ErrorClass = status, appfailure.ClassOf(status)
		return failureOutput(status, msg)
	}

	model := req.model
	row.Model = model
	if err := p.checkBody(kind, req); err != nil {
		return bad(http.StatusBadRequest, strings.ToUpper(err.Error()[:1])+err.Error()[1:])
	}
	ref, entry, ok := p.target(ctx, kind, model)
	if !ok {
		return bad(http.StatusNotFound, "Model not found")
	}
	row.Provider, row.Model = ref.Provider, ref.Model
	if kind == upstream.KindSTT {
		req.modelTransport = p.modelTransport(ref)
	}
	if !p.mayCall(in.Caller, ref) {
		return bad(http.StatusForbidden, appfailure.NotAllowed(model))
	}
	cfg := entry.Media.Config(kind)
	if cfg == nil {
		return bad(http.StatusBadRequest, "This provider does not serve that model here")
	}
	return p.serve(ctx, kind, in, ref, entry, cfg, req, ref.Model, row)
}

// mayCall reports whether the caller may use the resolved service. A key that is
// not restricted may call anything.
func (p *Pipeline) mayCall(caller identity.Caller, ref upstream.MediaRef) bool {
	if !caller.Restricted {
		return true
	}
	return p.d.Registry.Allows(caller.Allow, ref)
}

// record starts the one usage row a request leaves behind; the returned
// function finishes and queues it.
func (p *Pipeline) record(in Input) (*analytics.Record, func()) {
	row := &analytics.Record{
		Time: p.now(), RequestID: in.RequestID, APIKeyID: in.APIKeyID, APIKeyName: in.KeyName,
		Endpoint: in.Path,
	}
	started := p.now()
	return row, func() {
		row.LatencyMs = p.now().Sub(started).Milliseconds()
		if p.d.Usage != nil {
			p.d.Usage.Enqueue(*row)
		}
	}
}

// reject answers a request refused before any account was chosen, and leaves
// the one usage row every request owns.
func (p *Pipeline) reject(in Input, out Output) Output {
	row, done := p.record(in)
	row.Status, row.ErrorClass = out.Status, appfailure.ClassOf(out.Status)
	done()
	return out
}

// serve walks the provider's accounts for one resolved request. lockModel names
// the scope a failure locks: the model itself, or a capability's own scope so a
// failed search never takes chat offline.
func (p *Pipeline) serve(ctx context.Context, kind string, in Input, ref upstream.MediaRef, entry upstream.Entry, cfg *upstream.MediaConfig, req parsed, lockModel string, row *analytics.Record) Output {
	adapter, ok := p.d.Adapters.For(ref.Provider, kind, req.modelTransport)
	if !ok {
		row.Status, row.ErrorClass = http.StatusBadRequest, "invalid_request"
		return failureOutput(http.StatusBadRequest, "This provider does not serve that model here")
	}
	exclude := map[string]bool{}
	started := p.now()
	var last *Output
	for {
		conn, out, ok := p.selectAccount(ctx, kind, entry, ref, req.model, lockModel, in.ConnectionID, exclude, row, last)
		if !ok {
			return out
		}
		// A borrowed connection is reported against the provider that owns it.
		owner := ref
		if conn.Provider != "" {
			owner.Provider = conn.Provider
		}
		call := media.Request{
			Kind: kind, Provider: ref.Provider, Model: req.model, WireModel: p.wireModel(entry, cfg, ref.Model),
			Entry: entry, Config: cfg, Body: req.body, Upload: req.upload, ModelTransport: req.modelTransport,
			Action: req.action, ContentType: in.ContentType, IdempotencyKey: in.IdempotencyKey,
		}
		out, done := p.try(ctx, kind, adapter, entry, cfg, owner, lockModel, conn, call, row)
		if done {
			if kind == upstream.KindSearch || kind == upstream.KindFetch {
				out = stampTiming(out, p.now().Sub(started).Milliseconds())
			}
			return videoPin(out, kind, conn)
		}
		if conn.ID == "" {
			return failureOutput(http.StatusBadGateway, "The provider refused the request")
		}
		// A create is a billable job: only a refusal upstream raises before the
		// job exists may move to another account.
		if kind == upstream.KindVideo && !createRotates(out.Status) {
			return out
		}
		exclude[conn.ID] = true
		last = &out
	}
}

// createRotates reports whether a video create may be re-sent on another
// account: the statuses a provider raises before it starts a job.
func createRotates(status int) bool {
	return status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusTooManyRequests
}

// videoPin answers the account a job belongs to, which the client echoes when it
// polls.
func videoPin(out Output, kind string, conn repository.Connection) Output {
	if kind != upstream.KindVideo || conn.ID == "" || out.Status < 200 || out.Status > 299 {
		return out
	}
	h := make(map[string]string, len(out.Header)+2)
	for k, v := range out.Header {
		h[k] = v
	}
	h["x-9router-connection-id"] = conn.ID
	h["X-Rllm-Connection-Id"] = conn.ID
	out.Header = h
	return out
}

// selectAccount picks the next account. A service that takes no credential runs
// without one; every other one walks the provider's accounts, and a provider that
// borrows another's connection (ollama-search) falls back to it. prefer pins the
// request to one account while it is a candidate.
func (p *Pipeline) selectAccount(ctx context.Context, kind string, entry upstream.Entry, ref upstream.MediaRef, model, lockModel, prefer string, exclude map[string]bool, row *analytics.Record, last *Output) (repository.Connection, Output, bool) {
	cfg := entry.Media.Config(kind)
	noAuth := cfg != nil && (cfg.AuthType == "none" || (entry.Media != nil && entry.Media.NoAuth))
	if p.d.Selector == nil {
		if noAuth {
			return repository.Connection{}, Output{}, true
		}
		return repository.Connection{}, failureOutput(http.StatusNotFound, "No credentials for provider: "+ref.Provider), false
	}
	req := account.Request{Provider: ref.Provider, Model: lockModel, RequestedModel: model, Exclude: exclude, Prefer: prefer}
	sel, err := p.d.Selector.Select(ctx, req)
	if err != nil && errors.Is(err, account.ErrNoConnections) && entry.Media != nil && entry.Media.CredentialFallback != "" && p.d.Registry != nil {
		if id, ok := p.d.Registry.Resolve(entry.Media.CredentialFallback); ok {
			req.Provider = id
			sel, err = p.d.Selector.Select(ctx, req)
		}
	}
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
func (p *Pipeline) try(ctx context.Context, kind string, adapter media.Handler, entry upstream.Entry, cfg *upstream.MediaConfig, ref upstream.MediaRef, lockModel string, conn repository.Connection, call media.Request, row *analytics.Record) (Output, bool) {
	started := p.now()
	// A video poll reads a job the client names: a refusal says the id or the
	// job is wrong, never that the account is, so nothing is reported.
	// noReport covers it too, with a service that takes no credential.
	noReport := call.JobID != "" || (cfg != nil && (cfg.AuthType == "none" || (entry.Media != nil && entry.Media.NoAuth)))
	oauthed := conn.ID != "" && conn.AuthType == "oauth" && entry.OAuth != nil
	if oauthed && p.d.Refresher != nil {
		// Only a dead account stops the request: a token that looks due may still be
		// accepted, so any other failure goes on with the one it has.
		fresh, err := p.d.Refresher.Ready(ctx, conn)
		switch {
		case err == nil:
			conn = fresh
		case errors.Is(err, oauth.ErrExpired):
			return p.accountFailed(ctx, conn, ref, lockModel, http.StatusUnauthorized, row, nil, "", noReport)
		default:
			p.logRefresh(ref, err)
		}
	}
	mints := oauthed && entry.OAuth.Mint != "" && p.d.Minter != nil
	key := credential(conn, cfg)
	if mints {
		minted, err := p.d.Minter.Token(ctx, conn)
		if err != nil {
			return p.mintFailed(ctx, err, conn, ref, lockModel, row, noReport)
		}
		key = minted
	}
	call.Credential, call.Account = key, accountData(conn)
	req := call
	ans, refused, err := p.call(ctx, adapter, req)
	if refused {
		renewed, newKey, ok := p.renew(ctx, conn, entry, cfg, key, mints, row)
		if !ok {
			return p.accountFailed(ctx, conn, ref, lockModel, http.StatusUnauthorized, row, nil, "", noReport)
		}
		conn, req.Credential = renewed, newKey
		ans, refused, err = p.call(ctx, adapter, req)
		if refused {
			return p.accountFailed(ctx, conn, ref, lockModel, http.StatusUnauthorized, row, nil, "", noReport)
		}
	}
	p.keepRotated(ctx, conn, ref, ans)
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
			return p.accountFailed(ctx, conn, ref, lockModel, pe.Status, row, nil, pe.Text, noReport)
		}
		if ctx.Err() != nil {
			row.Status, row.ErrorClass = 499, "client_closed"
			return failureOutput(499, "The client closed the request"), true
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
	if !noReport {
		p.succeed(ctx, conn, lockModel, started)
	}
	return out, true
}

// mintFailed answers a failed token mint. An unreachable token service is not
// the account's fault, so it neither locks nor reports it; a refusal is.
func (p *Pipeline) mintFailed(ctx context.Context, err error, conn repository.Connection, ref upstream.MediaRef, lockModel string, row *analytics.Record, noReport bool) (Output, bool) {
	if ctx.Err() != nil {
		row.Status, row.ErrorClass = http.StatusGatewayTimeout, "timeout"
		return failureOutput(http.StatusGatewayTimeout, "The provider did not answer in time"), true
	}
	p.logRefresh(ref, err)
	if oauth.ClassOf(err) == oauth.ClassTransport {
		row.Status, row.ErrorClass = http.StatusBadGateway, "network"
		return failureOutput(http.StatusBadGateway, "The provider could not be reached"), true
	}
	return p.accountFailed(ctx, conn, ref, lockModel, http.StatusUnauthorized, row, nil, "", noReport)
}

// logRefresh records a failed token call by its class, never its text.
func (p *Pipeline) logRefresh(ref upstream.MediaRef, err error) {
	if p.d.Log == nil {
		return
	}
	class := string(oauth.ClassOf(err))
	if class == "" {
		class = "other"
	}
	p.d.Log.Warningf("media: token call failed: provider=%s class=%s", ref.Provider, class)
}

// keepRotated stores a credential the provider rotated during the call. The
// provider has already retired the old value, so it is kept whether the call
// then succeeded or not; a store fault is logged and leaves the old value.
func (p *Pipeline) keepRotated(ctx context.Context, conn repository.Connection, ref upstream.MediaRef, ans media.Response) {
	if ans.NewCredential == "" || conn.ID == "" || p.d.Credentials == nil {
		return
	}
	if err := p.d.Credentials.UpdateApiKey(ctx, conn.ID, ans.NewCredential); err != nil && p.d.Log != nil {
		p.d.Log.Warningf("media: storing a rotated credential failed: provider=%s", ref.Provider)
	}
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

func (p *Pipeline) call(ctx context.Context, adapter media.Handler, req media.Request) (media.Response, bool, error) {
	client := p.d.Client
	if req.Kind == upstream.KindFetch && p.d.FetchClient != nil {
		client = p.d.FetchClient
	}
	if client == nil {
		return media.Response{}, false, errors.New("media: no client")
	}
	cctx, cancel := context.WithTimeout(ctx, p.timeout(req.Kind, req.Config))
	defer cancel()
	req.Network = p.d.Network
	if direct, ok := adapter.(media.Director); ok {
		ans, err := direct.Direct(cctx, req, client)
		return ans, false, p.timedOut(ctx, cctx, err)
	}
	a, ok := adapter.(media.Adapter)
	if !ok {
		return media.Response{}, false, errors.New("media: the provider's handler serves neither shape")
	}
	call, err := a.Build(cctx, req, client)
	if err != nil {
		return media.Response{}, false, err
	}
	status, header, body, err := client.Do(cctx, call.Method, call.URL, call.Header, call.Body)
	if err != nil {
		return media.Response{}, false, p.timedOut(ctx, cctx, err)
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		// The adapter never reads a refusal: the gateway renews and retries.
		return media.Response{}, true, nil
	}
	out, err := a.Parse(cctx, req, p.d.Client, status, header, body)
	return out, false, err
}

// timedOut maps an error to the deadline that caused it. A call that outran its
// own bound is the provider being slow: a 504. A request whose client has gone
// stays the context's error, which try answers as a closed client.
func (p *Pipeline) timedOut(ctx, cctx context.Context, err error) error {
	if err == nil || ctx.Err() != nil || cctx.Err() == nil {
		return err
	}
	var pe *media.ProviderError
	if errors.As(err, &pe) {
		return err
	}
	return &media.ProviderError{Status: http.StatusGatewayTimeout, Text: "The provider did not answer in time"}
}

// accountFailed reports the failure and says whether this answer is final. A
// refusal the reporter lets another account serve is not.
func (p *Pipeline) accountFailed(ctx context.Context, conn repository.Connection, ref upstream.MediaRef, lockModel string, status int, row *analytics.Record, out *Output, msg string, noAuth bool) (Output, bool) {
	if noAuth || conn.ID == "" || p.d.Reporter == nil {
		return p.refusalOutput(status, row, out, msg), true
	}
	res, err := p.d.Reporter.Fail(ctx, account.FailInput{
		ConnID: conn.ID, Provider: ref.Provider, Model: lockModel, Status: status,
	})
	if err == nil && res.Fallback {
		row.Status, row.ErrorClass = appfailure.ClientStatus(status), appfailure.ClassOf(status)
		return failureOutput(appfailure.ClientStatus(status), refusalText(msg)), false
	}
	return p.refusalOutput(status, row, out, msg), true
}

// refusalOutput is the client's answer for a failure that ends the request.
func (p *Pipeline) refusalOutput(status int, row *analytics.Record, out *Output, msg string) Output {
	if out != nil {
		return *out
	}
	client := appfailure.ClientStatus(status)
	row.Status, row.ErrorClass = client, appfailure.ClassOf(status)
	return failureOutput(client, refusalText(msg))
}

// refusalText is the adapter's fixed word, or the gateway's own.
func refusalText(msg string) string {
	if msg != "" {
		return msg
	}
	return "The provider refused the request"
}

// succeed clears what an older failure left. started is when the attempt began,
// so a failure a concurrent request wrote meanwhile is not cleared by it.
func (p *Pipeline) succeed(ctx context.Context, conn repository.Connection, model string, started time.Time) {
	if conn.ID == "" || p.d.Reporter == nil {
		return
	}
	if err := p.d.Reporter.Succeed(ctx, conn.ID, model, started); err != nil && p.d.Log != nil {
		p.d.Log.Warningf("media: clearing an account's failure state failed: provider=%s", conn.Provider)
	}
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

// sizePattern is a width and a height, as OpenAI's sizes and the vendors' own
// aspect strings are written.
var sizePattern = regexp.MustCompile(`^(auto|[0-9]{1,4}x[0-9]{1,4})$`)

// Bounds on what one media request may carry.
const (
	MaxInputs    = 2048
	MaxInputText = 32 << 10
	MaxImages    = 10
	// MaxFetchBytes bounds one page the built-in converter reads.
	MaxFetchBytes = 5 << 20
)

// checkJSONBody refuses what an embeddings or image request cannot serve.
func checkJSONBody(kind string, body []byte) error {
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
