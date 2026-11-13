package pipeline

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/gateway/conversation"
	"rllmx/app/llm/oauth"
	"rllmx/app/llm/translate"
	"rllmx/app/llm/upstream"
	"rllmx/app/store/repository"
)

// Refresher renews an OAuth account's access token: before a request when it is
// due, and after the provider refused it.
type Refresher interface {
	Ready(ctx context.Context, conn repository.Connection) (repository.Connection, error)
	Renew(ctx context.Context, conn repository.Connection) (repository.Connection, error)
}

// Minter makes the second credential a login's token is traded for, which is the
// one a request carries.
type Minter interface {
	Token(ctx context.Context, conn repository.Connection) (string, error)
	Renew(ctx context.Context, conn repository.Connection, rejected string) (string, error)
}

// mints reports whether this connection's requests carry a minted credential.
func (c *call) mints(conn repository.Connection) bool {
	return c.p.d.Minter != nil && c.signedIn(conn) && c.dest.entry.OAuth.Mint != ""
}

// keyFor is the credential a request on this connection carries: the stored one,
// or for a provider with a mint the minted one. A mint that fails is the
// account's failure and carries no provider text.
func (c *call) keyFor(ctx context.Context, conn repository.Connection) (string, *accountFail) {
	if !c.mints(conn) {
		return credential(conn), nil
	}
	key, err := c.p.d.Minter.Token(ctx, conn)
	if err != nil {
		return "", c.mintFailed(ctx, err)
	}
	return key, nil
}

func (c *call) mintFailed(ctx context.Context, err error) *accountFail {
	if ctx.Err() != nil {
		return &accountFail{status: http.StatusGatewayTimeout, class: "timeout", text: "The upstream did not answer in time"}
	}
	c.logRefresh(err)
	if oauth.ClassOf(err) == oauth.ClassTransport {
		return &accountFail{status: http.StatusBadGateway, class: "network", text: "The upstream could not be reached"}
	}
	return &accountFail{status: http.StatusUnauthorized, class: "auth", text: "no credentials"}
}

// renew answers a refusal: a mint is replaced once, any other login is renewed.
// The connection and the credential for the second send come back.
func (c *call) renew(ctx context.Context, conn repository.Connection, rejected string) (repository.Connection, string, error) {
	if c.mints(conn) {
		key, err := c.p.d.Minter.Renew(ctx, conn, rejected)
		return conn, key, err
	}
	fresh, err := c.p.d.Refresher.Renew(ctx, conn)
	return fresh, credential(fresh), err
}

// signedIn reports whether a request on this connection carries a login's token
// rather than a key: an OAuth account of a provider that has a login.
func (c *call) signedIn(conn repository.Connection) bool {
	return conn.AuthType == "oauth" && c.dest.entry.OAuth != nil
}

// sent is one upstream request as it leaves for one account: the address, the
// body, the headers a login adds, and what the answer needs to undo the
// disguise.
type sent struct {
	url  string
	body []byte
	auth *upstream.Auth
	// session is the conversation id the upstream sees: a keyed hash bound to
	// the account, never a value a client chose.
	session string
	// names maps each renamed tool back to the client's name; cloaked says the
	// body was disguised, so a name may be restored by its suffix.
	names   map[string]string
	cloaked bool
}

// prepare builds the request for a connection. An API key leaves exactly as the
// destination says. A login's token is sent as a bearer with the headers its
// provider's data names, and when the provider asks for it a request from
// another client is disguised as the tool's own.
func (c *call) prepare(conn repository.Connection) sent {
	out := sent{url: c.url(), body: c.body, auth: c.dest.auth}
	if c.dest.entry.Executor == upstream.ExecutorCodex || c.dest.entry.Executor == upstream.ExecutorGrokCLI {
		out.session = c.p.d.Sessions.Bound(c.session(conn), conn.ID)
	}
	if !c.signedIn(conn) {
		return out
	}
	o := c.dest.entry.OAuth
	token := credential(conn)
	headers := map[string]string{}
	if c.dest.auth != nil {
		maps.Copy(headers, c.dest.auth.Headers)
	}
	maps.Copy(headers, o.Headers)
	maps.Copy(headers, o.Identity)
	c.p.d.ClaudeCLI.Rewrite(headers)
	if o.Beta != nil {
		headers["anthropic-beta"] = betaList(o.Beta, c.dest.model, c.body, clientFlags(c.in.Header))
	}
	session := c.session(conn)
	if o.Disguise && c.dest.format == upstream.FormatClaude && c.client != ClientClaude && strings.HasPrefix(token, claudeOAuthPrefix) {
		body, names := translate.Cloak(c.body, &translate.Disguise{Seed: token, Session: session, Tools: true, Version: c.p.d.ClaudeCLI.Version()})
		out.body, out.names, out.cloaked = body, names, true
	}
	if o.Session != nil {
		if sid := sessionHeader(o.Session, session, conn.ID, c.p.d.Sessions, out.body); sid != "" {
			headers[o.Session.Name] = sid
		}
	}
	if v := accountHeader(conn, o.Account); v != "" {
		headers[o.Account.Name] = v
	}
	out.auth = c.loginAuth(headers)
	if o.URLSuffix != "" && !strings.Contains(out.url, "?") {
		out.url += o.URLSuffix
	}
	return out
}

// loginAuth presents a login's token. On a lane it is the lane's own scheme; on
// the provider's default address it is the scheme the provider's data states,
// and with none stated a bearer token, which is what the Claude and Codex logins
// send.
func (c *call) loginAuth(headers map[string]string) *upstream.Auth {
	scheme := &upstream.Auth{Scheme: upstream.SchemeBearer, Header: "Authorization"}
	switch {
	case c.dest.lane != nil:
		if c.dest.lane.Auth != nil {
			scheme = c.dest.lane.Auth
		}
	case c.dest.entry.Auth != nil:
		scheme = c.dest.entry.Auth
	}
	return &upstream.Auth{Scheme: scheme.Scheme, Header: scheme.Header, Headers: headers}
}

// claudeOAuthPrefix begins every access token of a Claude login.
const claudeOAuthPrefix = "sk-ant-oat"

// session is the conversation this request belongs to, resolved once the
// account is known because the last fallback is one id per account.
func (c *call) session(conn repository.Connection) string {
	return c.p.d.Sessions.Resolve(conversation.Input{Header: c.in.Header, Body: c.in.Body, ConnectionID: conn.ID})
}

// sessionHeader is the value of a login's session header. A keyed header sends a
// hash bound to the account, so a client cannot choose what an upstream sees; a
// plain one sends the id the user id carries, which is how Claude Code itself
// agrees with its body.
func sessionHeader(h *upstream.SessionHeader, session, connID string, store *conversation.Store, body []byte) string {
	if h.Keyed {
		return store.Bound(session, connID)
	}
	if id := translate.SessionOf(gjson.GetBytes(body, "metadata.user_id").String()); id != "" {
		return id
	}
	return strings.TrimPrefix(session, "claude:")
}

// ready renews the token first when it is due. Only a dead account stops the
// request; any other failure goes on with the token it has, because a token
// that looks due may still be accepted.
func (c *call) ready(ctx context.Context, conn repository.Connection) (repository.Connection, error) {
	if c.p.d.Refresher == nil || !c.signedIn(conn) {
		return conn, nil
	}
	fresh, err := c.p.d.Refresher.Ready(ctx, conn)
	switch {
	case err == nil:
		return fresh, nil
	case errors.Is(err, oauth.ErrExpired):
		return conn, err
	}
	c.logRefresh(err)
	return conn, nil
}

// retryable reports whether a refusal on this connection may be answered by
// renewing its token and sending the request once more.
func (c *call) retryable(conn repository.Connection, status int) bool {
	if status != http.StatusUnauthorized && status != http.StatusForbidden {
		return false
	}
	return c.mints(conn) || (c.p.d.Refresher != nil && c.signedIn(conn))
}

func (c *call) logRefresh(err error) {
	if c.p.d.Log == nil {
		return
	}
	reason := "failed"
	if class := oauth.ClassOf(err); class != "" {
		reason = string(class)
	}
	c.p.d.Log.Warningf("pipeline: renewing a %s token failed: %s", c.dest.provider, reason)
}
