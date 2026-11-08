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
	hooks     = map[string]bool{"": true, "codex": true}

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
)

// OAuth is how an account is connected by logging in. The client id is not
// here: it comes from configuration, so a deployer can replace it.
type OAuth struct {
	Flow         string `json:"flow"`
	AuthorizeURL string `json:"authorizeUrl"`
	TokenURL     string `json:"tokenUrl"`
	Scope        string `json:"scope"`
	// Params are extra authorize parameters, sent in this order after the
	// standard ones.
	Params []Param `json:"params,omitempty"`
	// Redirect is fixed (a listener on FixedPort serves CallbackPath) or local
	// (the router's own port serves LocalCallbackPath).
	Redirect     string `json:"redirect"`
	FixedPort    int    `json:"fixedPort,omitempty"`
	CallbackPath string `json:"callbackPath,omitempty"`
	// Exchange is the encoding of the code exchange body: json or form.
	Exchange string `json:"exchange"`
	// EchoState sends the login state in the exchange body.
	EchoState bool `json:"echoState,omitempty"`
	// Hook names a step run on the tokens to read the account's identity.
	Hook string `json:"hook,omitempty"`
	// Refresh says how the access token is renewed.
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
}

// Param is one extra authorize parameter.
type Param struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func (o *OAuth) validate() error {
	if o.Flow != FlowCodePKCE {
		return errors.New("oauth.flow is unknown")
	}
	for _, u := range []string{o.AuthorizeURL, o.TokenURL} {
		if err := httpsURL(u); err != nil {
			return err
		}
	}
	if strings.TrimSpace(o.Scope) == "" || text.HasASCIIControl(o.Scope) {
		return errors.New("oauth.scope is empty or holds a control character")
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
	if !hooks[o.Hook] {
		return errors.New("oauth.hook is unknown")
	}
	if !encodings[o.Refresh.Encoding] {
		return errors.New("oauth.refresh.encoding is unknown")
	}
	if o.Refresh.LeadSeconds < 0 || o.Refresh.LeadSeconds > maxLeadSeconds || o.Refresh.MaxAgeSeconds < 0 || o.Refresh.MaxAgeSeconds > maxAgeSeconds {
		return errors.New("oauth.refresh timing is out of range")
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
