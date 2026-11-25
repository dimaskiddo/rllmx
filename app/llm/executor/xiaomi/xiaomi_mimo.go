package xiaomi

import (
	"context"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"rllmx/app/llm/executor"
	"rllmx/app/llm/upstream"
)

// The account-service clusters. The cookie is fetched for the same cluster the
// chat goes to.
var mimoHosts = map[string]string{
	"cn":  "https://mimo-server-cn.xiaomimimo.com",
	"sgp": "https://mimo-server-sgp.xiaomimimo.com",
	"ams": "https://mimo-server-ams.xiaomimimo.com",
	"ru":  "https://mimo-server-ru.xiaomimimo.com",
	"in":  "https://mimo-server-in.xiaomimimo.com",
}

const mimoDefaultRegion = "sgp"

// mimoAccountModels are the ids the account service serves: they spend the
// desktop account's own quota rather than the key's.
var mimoAccountModels = map[string]bool{
	"mimo-v2.6-pro":            true,
	"mimo-v2.6-flash":          true,
	"mimo-v2.6-pro-ultraspeed": true,
}

// mimoUserAgent is what the account service expects of the desktop client.
const mimoUserAgent = "miNative PC/Normal Windows_NT/10.0.19045 DEVT/PC DEVS/Windows APP/miaccount_desktop APPV/0.1.0"

// MimoUserAgent is the desktop client's user agent, which the account service
// checks as well as the cookie.
const MimoUserAgent = mimoUserAgent

// MimoHost answers the account-service base for a region; an unknown region
// takes the default.
func MimoHost(region string) string {
	if host, ok := mimoHosts[region]; ok {
		return host
	}
	return mimoHosts[mimoDefaultRegion]
}

// XiaomiMimo serves the MiMo cloud API, and the account service for the three
// ids that spend a desktop account's quota when the account has a pass token.
type XiaomiMimo struct {
	Default *executor.Default
	Cookies executor.CookieSource
}

// Execute picks the route: the account service for its own ids when the account
// holds a pass token, the cloud API otherwise.
func (x *XiaomiMimo) Execute(ctx context.Context, req executor.Request) (executor.Response, error) {
	if !mimoAccountRoute(req) || x.Cookies == nil {
		return x.cloud(ctx, req)
	}
	cookie, err := x.Cookies.Cookie(ctx, executor.Field(req, "mimoPassToken"), executor.Field(req, "mimoUserId"), executor.Field(req, "mimoCUserId"), mimoRegion(req.Account), false)
	if err != nil {
		return executor.Response{}, err
	}
	resp, err := x.account(ctx, req, cookie)
	// A cached session can end early; one fresh cookie is tried before the
	// account is called unfit for a credential problem.
	if err == nil && resp.Status != http.StatusUnauthorized {
		return resp, nil
	}
	if resp.Body != nil && resp.Status == http.StatusUnauthorized {
		executor.Drain(resp.Body)
	}
	cookie, cerr := x.Cookies.Cookie(ctx, executor.Field(req, "mimoPassToken"), executor.Field(req, "mimoUserId"), executor.Field(req, "mimoCUserId"), mimoRegion(req.Account), true)
	if cerr != nil {
		return executor.Response{}, cerr
	}
	return x.account(ctx, req, cookie)
}

// cloud sends to the public API, where a key or a login's key is the credential.
func (x *XiaomiMimo) cloud(ctx context.Context, req executor.Request) (executor.Response, error) {
	if req.Key == "" {
		return executor.Response{}, executor.ErrNoCredential
	}
	return x.Default.Execute(ctx, req)
}

// account sends to the account service, authorized by the session cookie alone.
func (x *XiaomiMimo) account(ctx context.Context, req executor.Request, cookie string) (executor.Response, error) {
	req.URL = mimoHosts[mimoRegion(req.Account)] + "/api/route/chat/completions"
	req.Body = mimoAccountBody(req.Body)
	req.Auth = &upstream.Auth{Headers: map[string]string{
		"Cookie":     cookie,
		"User-Agent": mimoUserAgent,
	}}
	return x.Default.Execute(ctx, req)
}

// mimoAccountRoute reports a request the account service serves: one of its ids
// on an account that holds a pass token.
func mimoAccountRoute(req executor.Request) bool {
	if executor.Field(req, "mimoPassToken") == "" {
		return false
	}
	id := req.Model
	if i := strings.IndexByte(id, '/'); i >= 0 {
		id = id[i+1:]
	}
	return mimoAccountModels[id]
}

// mimoRegion is the account's cluster, defaulting to the open one.
func mimoRegion(account func(string) string) string {
	if account == nil {
		return mimoDefaultRegion
	}
	if r := strings.ToLower(strings.TrimSpace(account("region"))); r != "" {
		if _, ok := mimoHosts[r]; ok {
			return r
		}
	}
	return mimoDefaultRegion
}

// mimoAccountBody rewrites the thinking request into the field the account
// service reads and fills the sampling defaults its own client sends.
func mimoAccountBody(body []byte) []byte {
	out, err := body, error(nil)
	if effort := gjson.GetBytes(body, "reasoning_effort"); effort.Exists() {
		level := strings.ToLower(effort.String())
		if level == "xhigh" {
			level = "high"
		}
		if out, err = sjson.DeleteBytes(out, "reasoning_effort"); err != nil {
			return body
		}
		if out, err = sjson.SetBytes(out, "output_config.effort", level); err != nil {
			return body
		}
	}
	if !gjson.GetBytes(out, "temperature").Exists() {
		if out, err = sjson.SetBytes(out, "temperature", 1.0); err != nil {
			return body
		}
	}
	if !gjson.GetBytes(out, "top_p").Exists() {
		if out, err = sjson.SetBytes(out, "top_p", 0.95); err != nil {
			return body
		}
	}
	return out
}
