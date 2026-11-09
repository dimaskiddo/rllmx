package upstream

import (
	"errors"
	"net/url"
	"regexp"
	"strings"

	"rllmx/app/text"
)

// Login flows and redirect policies a data file may name.
const (
	FlowCodePKCE = "code-pkce"
	FlowDevice   = "device"

	// MintCopilot names the second credential a GitHub login is exchanged for.
	MintCopilot = "copilot"
	// Endpoint roles a hook or the mint calls.
	EndpointCopilotToken = "copilotToken"
	EndpointUser         = "user"
	EndpointKey          = "key"
	EndpointAgentKey     = "agentKey"
	EndpointProfile      = "profile"
	// EndpointVerify is the page a device login falls back to when the provider
	// names none.
	EndpointVerify = "verify"

	// DeviceIDToken is the one placeholder a call header may hold: the id made for
	// the account when its device login starts.
	DeviceIDToken = "{deviceId}"

	RedirectFixed = "fixed"
	RedirectLocal = "local"

	EncodingJSON = "json"
	EncodingForm = "form"

	// LocalCallbackPath is where the router itself serves the callback page.
	LocalCallbackPath = "/callback"
)

var (
	redirects = map[string]bool{RedirectFixed: true, RedirectLocal: true}
	encodings = map[string]bool{EncodingJSON: true, EncodingForm: true}
	hooks     = map[string]bool{"": true, "codex": true, "github": true, "muse": true, "nous": true, "kilocode": true, "grokCli": true}
	mints     = map[string]bool{"": true, MintCopilot: true}
	// refreshHooks run after a token renewal; dialects replace the start and the
	// poll of a device login that does not follow RFC 8628.
	refreshHooks = map[string]bool{"": true, "nous": true}
	dialects     = map[string]bool{"": true, "kilocode": true}

	paramName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	// ownParams are set by the login itself, so a data file cannot name them.
	ownParams = map[string]bool{
		"response_type": true, "client_id": true, "redirect_uri": true, "scope": true,
		"code_challenge": true, "code_challenge_method": true, "state": true,
	}
)

const (
	maxOAuthParams = 8
	maxParamValue  = 256
	minFixedPort   = 1024

	maxIdentityHeaders = 24
	maxBetaFlags       = 32

	maxLeadSeconds  = 7 * 24 * 3600
	maxAgeSeconds   = 90 * 24 * 3600
	maxOAuthHeaders = 8
	maxCallHeaders  = 8
	maxEndpoints    = 8
)

// OAuth is how an account is connected by logging in. The client id is not
// here: it comes from configuration, so a deployer can replace it.
type OAuth struct {
	Flow         string `json:"flow"`
	AuthorizeURL string `json:"authorizeUrl,omitempty"`
	// DeviceURL starts a device login, which the user then approves on the
	// provider's own page.
	DeviceURL string `json:"deviceUrl,omitempty"`
	TokenURL  string `json:"tokenUrl"`
	Scope     string `json:"scope"`
	// Params are extra authorize parameters, sent in this order after the
	// standard ones.
	Params []Param `json:"params,omitempty"`
	// Redirect is fixed (a listener on FixedPort serves CallbackPath) or local
	// (the router's own port serves LocalCallbackPath).
	Redirect     string `json:"redirect,omitempty"`
	FixedPort    int    `json:"fixedPort,omitempty"`
	CallbackPath string `json:"callbackPath,omitempty"`
	// Exchange is the encoding of the code exchange body: json or form.
	Exchange string `json:"exchange,omitempty"`
	// EchoState sends the login state in the exchange body.
	EchoState bool `json:"echoState,omitempty"`
	// Hook names a step run on the tokens to read the account's identity.
	Hook string `json:"hook,omitempty"`
	// Endpoints are the addresses a hook or a mint calls, by role. Each is https.
	Endpoints map[string]string `json:"endpoints,omitempty"`
	// Mint names a second credential made from the login's token, which is then
	// the one a request carries.
	Mint string `json:"mint,omitempty"`
	// Dialect names a device login that does not follow RFC 8628.
	Dialect string `json:"dialect,omitempty"`
	// NoClient marks a login the provider runs without a client id.
	NoClient bool `json:"noClient,omitempty"`
	// CallHeaders go on the calls the router makes to the provider while logging
	// in and renewing: the device start, the poll, the renewal and a hook. The
	// value may hold the device id placeholder.
	CallHeaders map[string]string `json:"callHeaders,omitempty"`
	// Refresh says how the access token is renewed. A device login may omit it.
	Refresh Refresh `json:"refresh"`
	// Headers are static headers sent with every request made on a login's
	// token. None is a credential or framing header.
	Headers map[string]string `json:"headers,omitempty"`
	// Identity are the headers that make a request look like the tool the login
	// belongs to: its user agent and version. They are data, never copied from
	// the client.
	Identity map[string]string `json:"identity,omitempty"`
	// URLSuffix is added to the provider's address for a login's token only.
	URLSuffix string `json:"urlSuffix,omitempty"`
	// Beta is the list of beta flags computed for each request.
	Beta *Beta `json:"beta,omitempty"`
	// Session names the header that carries the conversation.
	Session *SessionHeader `json:"session,omitempty"`
	// Account names the header that carries the account the login belongs to.
	Account *AccountHeader `json:"account,omitempty"`
	// Disguise makes a request from another client look like the tool's own:
	// tools renamed, decoys added, a billing block and a user id.
	Disguise bool `json:"disguise,omitempty"`
}

// Beta is how the beta flag list of a request is built.
type Beta struct {
	Base []string `json:"base"`
	// Heavy flags are added for a model that starts with one of HeavyPrefixes.
	Heavy         []string `json:"heavy,omitempty"`
	HeavyPrefixes []string `json:"heavyPrefixes,omitempty"`
	// Summary is the flag dropped when the client asks for thinking summaries.
	Summary string `json:"summary,omitempty"`
}

// SessionHeader says how the conversation id is sent. Keyed sends a hash bound
// to the connection instead of the id, so a client cannot choose the value an
// upstream sees.
type SessionHeader struct {
	Name  string `json:"name"`
	Keyed bool   `json:"keyed,omitempty"`
}

// AccountHeader names the header and the stored fields it is read from, first
// non-empty wins.
type AccountHeader struct {
	Name string   `json:"name"`
	From []string `json:"from"`
}

// Refresh is how and when a login's access token is renewed.
type Refresh struct {
	// Encoding is the body encoding of the refresh call: json or form.
	Encoding string `json:"encoding"`
	// LeadSeconds renews a token this long before it expires.
	LeadSeconds int `json:"leadSeconds"`
	// MaxAgeSeconds renews a token whose last refresh is older than this, even
	// when its expiry is far off. Zero means no such rule.
	MaxAgeSeconds int `json:"maxAgeSeconds,omitempty"`
	// TokenHeader names a header that also carries the refresh token.
	TokenHeader string `json:"tokenHeader,omitempty"`
	// Hook names a step run on a renewal's answer, from a table of its own.
	Hook string `json:"hook,omitempty"`
}

// Param is one extra authorize parameter.
type Param struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func (o *OAuth) validate() error {
	first := o.AuthorizeURL
	switch o.Flow {
	case FlowCodePKCE:
	case FlowDevice:
		first = o.DeviceURL
	default:
		return errors.New("oauth.flow is unknown")
	}
	for _, u := range []string{first, o.TokenURL} {
		if err := httpsURL(u); err != nil {
			return err
		}
	}
	// A dialect builds its own start, so it may send no scope.
	if (strings.TrimSpace(o.Scope) == "" && o.Dialect == "") || text.HasASCIIControl(o.Scope) {
		return errors.New("oauth.scope is empty or holds a control character")
	}
	if err := o.validateFlow(); err != nil {
		return err
	}
	if !hooks[o.Hook] {
		return errors.New("oauth.hook is unknown")
	}
	if err := o.validateMint(); err != nil {
		return err
	}
	if err := o.validateRefresh(); err != nil {
		return err
	}
	return o.validateRest()
}

// validateRefresh checks the renewal block. A device login the provider never
// renews has none.
func (o *OAuth) validateRefresh() error {
	r := o.Refresh
	if o.Flow == FlowDevice && r == (Refresh{}) {
		return nil
	}
	if !encodings[r.Encoding] {
		return errors.New("oauth.refresh.encoding is unknown")
	}
	if r.LeadSeconds < 0 || r.LeadSeconds > maxLeadSeconds || r.MaxAgeSeconds < 0 || r.MaxAgeSeconds > maxAgeSeconds {
		return errors.New("oauth.refresh timing is out of range")
	}
	if r.TokenHeader != "" && !loginHeader(r.TokenHeader) {
		return errors.New("oauth.refresh.tokenHeader is not allowed")
	}
	if !refreshHooks[r.Hook] {
		return errors.New("oauth.refresh.hook is unknown")
	}
	return nil
}

// validateFlow checks the fields that belong to one kind of login and refuses
// the ones that belong to the other.
func (o *OAuth) validateFlow() error {
	if o.Flow == FlowDevice {
		if o.AuthorizeURL != "" || o.Redirect != "" || o.FixedPort != 0 || o.CallbackPath != "" || o.Exchange != "" || o.EchoState {
			return errors.New("a device login has no authorize address, redirect or exchange")
		}
		return nil
	}
	if o.DeviceURL != "" || o.Dialect != "" || o.NoClient || len(o.CallHeaders) > 0 {
		return errors.New("oauth.deviceUrl, dialect, noClient and callHeaders belong to the device flow")
	}
	if !redirects[o.Redirect] {
		return errors.New("oauth.redirect is unknown")
	}
	switch o.Redirect {
	case RedirectFixed:
		if o.FixedPort < minFixedPort || o.FixedPort > 65535 {
			return errors.New("oauth.fixedPort must be 1024 to 65535")
		}
		if !strings.HasPrefix(o.CallbackPath, "/") || strings.ContainsAny(o.CallbackPath, "?#\\ ") || text.HasASCIIControl(o.CallbackPath) {
			return errors.New("oauth.callbackPath must be an absolute path")
		}
	case RedirectLocal:
		if o.FixedPort != 0 || o.CallbackPath != "" {
			return errors.New("oauth.fixedPort and callbackPath belong to the fixed redirect")
		}
	}
	if !encodings[o.Exchange] {
		return errors.New("oauth.exchange is unknown")
	}
	return nil
}

// validateMint checks the endpoints and the mint they serve.
func (o *OAuth) validateMint() error {
	if !mints[o.Mint] {
		return errors.New("oauth.mint is unknown")
	}
	if len(o.Endpoints) > maxEndpoints {
		return errors.New("oauth.endpoints has too many entries")
	}
	for name, u := range o.Endpoints {
		if !fieldName.MatchString(name) {
			return errors.New("oauth.endpoints holds a bad name")
		}
		if err := httpsURL(u); err != nil {
			return err
		}
	}
	if o.Mint == MintCopilot && o.Endpoints[EndpointCopilotToken] == "" {
		return errors.New("oauth.mint needs the copilotToken endpoint")
	}
	return nil
}

// validateCallHeaders checks the headers of the router's own calls. A value may
// hold the device id placeholder and nothing else in braces.
func (o *OAuth) validateCallHeaders() error {
	if !dialects[o.Dialect] {
		return errors.New("oauth.dialect is unknown")
	}
	if len(o.CallHeaders) > maxCallHeaders {
		return errors.New("oauth.callHeaders has too many entries")
	}
	for k, v := range o.CallHeaders {
		rest := strings.ReplaceAll(v, DeviceIDToken, "")
		if !loginHeader(k) || len(v) > maxHeaderBytes || text.HasASCIIControl(v) || strings.ContainsAny(rest, "{}") {
			return errors.New("an oauth call header is not allowed")
		}
	}
	return nil
}

// UsesDeviceID reports whether a call header asks for the account's device id.
func (o *OAuth) UsesDeviceID() bool {
	for _, v := range o.CallHeaders {
		if strings.Contains(v, DeviceIDToken) {
			return true
		}
	}
	return false
}

// validateRest checks what every login shares: headers, address suffix, beta
// list, session and account headers, and extra parameters.
func (o *OAuth) validateRest() error {
	if err := o.validateCallHeaders(); err != nil {
		return err
	}
	if len(o.Headers) > maxOAuthHeaders {
		return errors.New("oauth.headers has too many entries")
	}
	if err := validHeaders(o.Headers); err != nil {
		return err
	}
	if len(o.Identity) > maxIdentityHeaders {
		return errors.New("oauth.identity has too many entries")
	}
	if err := validHeaders(o.Identity); err != nil {
		return err
	}
	if o.URLSuffix != "" && (!strings.HasPrefix(o.URLSuffix, "?") || len(o.URLSuffix) > 64 || strings.ContainsFunc(o.URLSuffix, func(r rune) bool { return r <= ' ' || r == '#' || r == 0x7f })) {
		return errors.New("oauth.urlSuffix must be a short query string")
	}
	if err := o.Beta.validate(); err != nil {
		return err
	}
	if o.Session != nil && !loginHeader(o.Session.Name) {
		return errors.New("oauth.session.name is not allowed")
	}
	if a := o.Account; a != nil {
		if !loginHeader(a.Name) || len(a.From) == 0 || len(a.From) > 4 {
			return errors.New("oauth.account is not allowed")
		}
		for _, f := range a.From {
			if !fieldName.MatchString(f) {
				return errors.New("oauth.account.from holds a bad field name")
			}
		}
	}
	if len(o.Params) > maxOAuthParams {
		return errors.New("oauth.params has too many entries")
	}
	seen := map[string]bool{}
	for _, p := range o.Params {
		if !paramName.MatchString(p.Name) || ownParams[p.Name] || seen[p.Name] {
			return errors.New("an oauth parameter name is not allowed")
		}
		if len(p.Value) > maxParamValue || text.HasASCIIControl(p.Value) {
			return errors.New("an oauth parameter value is too long or holds a control character")
		}
		seen[p.Name] = true
	}
	return nil
}

// HookNames lists the login steps a data file may name.
func HookNames() []string { return names(hooks) }

// RefreshHookNames and DialectNames list the names a data file may use for a
// renewal step and for a device login that does not follow RFC 8628.
func RefreshHookNames() []string { return names(refreshHooks) }
func DialectNames() []string     { return names(dialects) }

func names(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for n := range set {
		if n != "" {
			out = append(out, n)
		}
	}
	return out
}

// BetaFlag reports whether s is a plain beta-flag token: a client may send it,
// and it may be written into a header without changing the header's meaning.
func BetaFlag(s string) bool { return betaFlag.MatchString(s) }

var (
	betaFlag  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	fieldName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]{0,63}$`)
	// loginHeaderName allows an underscore, which Codex's session header has and
	// HeaderName does not.
	loginHeaderName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)
)

// loginHeader reports a header name a login may set: a plain token that is not
// a framing or credential header.
func loginHeader(n string) bool {
	return loginHeaderName.MatchString(n) && !ReservedHeader(n) && !strings.EqualFold(n, "x-api-key")
}

func validHeaders(m map[string]string) error {
	for k, v := range m {
		if !loginHeader(k) || len(v) > maxHeaderBytes || text.HasASCIIControl(v) {
			return errors.New("an oauth header is not allowed")
		}
	}
	return nil
}

func (b *Beta) validate() error {
	if b == nil {
		return nil
	}
	if len(b.Base)+len(b.Heavy) > maxBetaFlags || len(b.HeavyPrefixes) > 8 {
		return errors.New("oauth.beta has too many entries")
	}
	flags := append(append([]string{}, b.Base...), b.Heavy...)
	if b.Summary != "" {
		flags = append(flags, b.Summary)
	}
	for _, f := range flags {
		if !betaFlag.MatchString(f) {
			return errors.New("an oauth beta flag is not allowed")
		}
	}
	for _, p := range b.HeavyPrefixes {
		if p == "" || len(p) > 64 || strings.ContainsFunc(p, func(r rune) bool { return r <= ' ' || r == 0x7f || r == ',' }) {
			return errors.New("an oauth beta prefix is not allowed")
		}
	}
	return nil
}

func httpsURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" {
		return errors.New("an oauth address must be a plain https URL")
	}
	return nil
}
