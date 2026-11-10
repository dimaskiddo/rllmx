package oauth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/platform/text"
	"rllmx/app/store/document"
)

const (
	zcodePollBytes = 32
	zcodeInterval  = 3 * time.Second
	zcodeLife      = 5 * time.Minute
	zcodeProvider  = "zai"
	zcodeKeyName   = "zcode-api-key"
	// teamProject is the project type that belongs to a team, which a personal
	// key is not made in.
	teamProject = "2"
)

// Endpoint roles of the ZCode login.
const (
	endpointBusinessLogin = "businessLogin"
	endpointAPI           = "api"
)

// zcodeID is the shape of an id taken from an answer before it enters a path.
var zcodeID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// zcodeStart opens a poll with a one-off token of its own. The token proves to
// the provider that the same client asks again; it is kept with the login and
// never leaves this process.
func zcodeStart(ctx context.Context, c *TokenClient, f Flow, h map[string]string) (DeviceGrant, error) {
	b := make([]byte, zcodePollBytes)
	if _, err := rand.Read(b); err != nil {
		return DeviceGrant{}, err
	}
	poll := hex.EncodeToString(b)
	status, raw, err := c.postJSON(ctx, f.DeviceURL, jsonBody(map[string]string{"provider": zcodeName(f)}), WithEntry(h, "Authorization", "Bearer "+poll))
	if err != nil {
		return DeviceGrant{}, err
	}
	if status < 200 || status > 299 {
		return DeviceGrant{}, &Error{StatusClass(status)}
	}
	body, ok := objectOf(raw)
	if !ok || !zcodeOK(body) {
		return DeviceGrant{}, &Error{ClassMalformed}
	}
	flow := body.Get("data.flow_id").String()
	page := body.Get("data.authorize_url").String()
	if !zcodeID.MatchString(flow) || !plainHTTPS(page) {
		return DeviceGrant{}, &Error{ClassMalformed}
	}
	g := DeviceGrant{DeviceCode: flow, VerificationURI: page, Extra: poll, ExpiresIn: zcodeLife, Interval: zcodeInterval}
	if n := body.Get("data.poll_interval_sec").Int(); n > 0 {
		g.Interval = clampInterval(n)
	}
	if at := body.Get("data.expires_at").Int(); at > 0 {
		if at < epochMillis {
			at *= 1000
		}
		if left := time.UnixMilli(at).Sub(c.now()); left > 0 {
			g.ExpiresIn = clampLife(int64(left.Seconds()))
		}
	}
	return g, nil
}

func zcodeName(f Flow) string {
	if p := paramOf(f, "provider"); p != "" {
		return p
	}
	return zcodeProvider
}

// zcodeOK reads the provider's success convention: no code, or 0 or 200.
func zcodeOK(body gjson.Result) bool {
	code := body.Get("code")
	switch code.Type {
	case gjson.Null:
		return true
	case gjson.Number, gjson.String:
		return code.String() == "0" || code.String() == "200"
	}
	return false
}

// zcodePoll asks whether the user approved. A ready answer holds the Z.ai
// token, which is traded for the plan key; any failure on the way ends the login
// with a fixed class and stores nothing.
func zcodePoll(ctx context.Context, c *TokenClient, f Flow, s DeviceState, h map[string]string) (Tokens, PollState, error) {
	if !zcodeID.MatchString(s.Code) || s.Extra == "" {
		return Tokens{}, pollDone, &Error{ClassMalformed}
	}
	status, raw, err := c.get(ctx, strings.TrimRight(f.TokenURL, "/")+"/"+url.PathEscape(s.Code), WithEntry(h, "Authorization", "Bearer "+s.Extra))
	if err != nil {
		return Tokens{}, pollDone, err
	}
	if status < 200 || status > 299 {
		return Tokens{}, pollDone, &Error{StatusClass(status)}
	}
	body, ok := objectOf(raw)
	if !ok || !zcodeOK(body) {
		return Tokens{}, pollDone, &Error{ClassRejected}
	}
	switch body.Get("data.status").String() {
	case "failed":
		return Tokens{}, pollDone, &Error{ClassDenied}
	case "ready":
	default:
		return Tokens{}, pollPending, nil
	}
	data := body.Get("data")
	zai := document.FirstString(data.Get(zcodeName(f)+".access_token"), data.Get(zcodeName(f)+".accessToken"), data.Get("accessToken"), data.Get("access_token"))
	if !bearerText(zai) {
		return Tokens{}, pollDone, &Error{ClassRejected}
	}
	key, err := zcodePlanKey(ctx, c, f, zai)
	if err != nil {
		return Tokens{}, pollDone, err
	}
	psd := map[string]string{"authMethod": "cli_poll"}
	user := data.Get("user")
	setText(psd, "username", shortText(user.Get("name")))
	setText(psd, "userId", shortText(user.Get("user_id")))
	return Tokens{Access: key, Email: CleanEmail(user.Get("email").String()), PSD: jsonObject(psd)}, pollDone, nil
}

// shortText returns a short, control-free string value, or "".
func shortText(v gjson.Result) string {
	s := strings.TrimSpace(v.String())
	if v.Type != gjson.String || len(s) > maxProfileField || text.HasASCIIControl(s) {
		return ""
	}
	return s
}

// zcodePlanKey trades the Z.ai token for a coding-plan key: a business login, the
// personal organisation and project, the key named for this router (made when
// missing) and its secret half.
func zcodePlanKey(ctx context.Context, c *TokenClient, f Flow, zai string) (string, error) {
	api := strings.TrimRight(f.Endpoints[endpointAPI], "/")
	login := f.Endpoints[endpointBusinessLogin]
	if api == "" || login == "" {
		return "", &Error{ClassMalformed}
	}
	data, err := zcodeBusiness(ctx, c, "POST", login, jsonBody(map[string]string{"token": zai}), nil)
	if err != nil {
		return "", err
	}
	business := document.FirstString(data.Get("access_token"), data.Get("accessToken"))
	if !bearerText(business) {
		return "", &Error{ClassMalformed}
	}
	auth := map[string]string{"Authorization": "Bearer " + business}
	info, err := zcodeBusiness(ctx, c, "GET", api+"/api/biz/customer/getCustomerInfo", "", auth)
	if err != nil {
		return "", err
	}
	org, project, ok := zcodeLocation(info)
	if !ok {
		return "", &Error{ClassRejected}
	}
	list := api + "/api/biz/v1/organization/" + url.PathEscape(org) + "/projects/" + url.PathEscape(project) + "/api_keys"
	keys, err := zcodeBusiness(ctx, c, "GET", list, "", auth)
	if err != nil {
		return "", err
	}
	name := paramOf(f, "key_name")
	if name == "" {
		name = zcodeKeyName
	}
	entry := gjson.Result{}
	for _, k := range keys.Array() {
		if k.Get("name").String() == name {
			entry = k
			break
		}
	}
	if !entry.Exists() {
		if entry, err = zcodeBusiness(ctx, c, "POST", list, jsonBody(map[string]string{"name": name}), auth); err != nil {
			return "", err
		}
	}
	apiKey := strings.TrimSpace(entry.Get("apiKey").String())
	if !zcodeSecretPart(apiKey) {
		return "", &Error{ClassMalformed}
	}
	secret, err := zcodeBusiness(ctx, c, "GET", list+"/copy/"+url.PathEscape(apiKey), "", auth)
	if err != nil {
		return "", err
	}
	half := strings.TrimSpace(secret.Get("secretKey").String())
	if !zcodeSecretPart(half) {
		return "", &Error{ClassMalformed}
	}
	return apiKey + "." + half, nil
}

// zcodeSecretPart reports one half of the plan key: text without a dot, space or
// control character.
func zcodeSecretPart(s string) bool {
	return s != "" && len(s) <= maxCredential/2 && !strings.ContainsAny(s, ". \t") && !text.HasASCIIControl(s)
}

// zcodeBusiness makes one call to the business API. Its answers carry
// {code, msg, data}; the data is returned, or the whole answer when there is
// none.
func zcodeBusiness(ctx context.Context, c *TokenClient, method, target, body string, headers map[string]string) (gjson.Result, error) {
	var status int
	var raw []byte
	var err error
	if method == "GET" {
		status, raw, err = c.get(ctx, target, headers)
	} else {
		status, raw, err = c.postJSON(ctx, target, body, headers)
	}
	if err != nil {
		return gjson.Result{}, err
	}
	if status < 200 || status > 299 {
		return gjson.Result{}, &Error{StatusClass(status)}
	}
	answer, ok := objectOf(raw)
	if !ok {
		return gjson.Result{}, &Error{ClassMalformed}
	}
	if !zcodeOK(answer) || answer.Get("success").Type == gjson.False {
		return gjson.Result{}, &Error{ClassRejected}
	}
	if d := answer.Get("data"); d.Exists() && d.Type != gjson.Null {
		return d, nil
	}
	return answer, nil
}

// zcodeLocation picks the personal organisation and project: the default-named
// ones when there are any, else the first that has a project outside a team.
func zcodeLocation(info gjson.Result) (org, project string, ok bool) {
	var orgs []struct {
		id, name string
		projects []gjson.Result
	}
	for _, o := range info.Get("organizations").Array() {
		var keep []gjson.Result
		for _, p := range o.Get("projects").Array() {
			if strings.TrimSpace(p.Get("projectType").String()) != teamProject {
				keep = append(keep, p)
			}
		}
		if id := o.Get("organizationId").String(); zcodeID.MatchString(id) && len(keep) > 0 {
			orgs = append(orgs, struct {
				id, name string
				projects []gjson.Result
			}{id, o.Get("organizationName").String(), keep})
		}
	}
	if len(orgs) == 0 {
		return "", "", false
	}
	chosen := orgs[0]
	for _, o := range orgs {
		if zcodeDefault(o.name) {
			chosen = o
			break
		}
	}
	pick := chosen.projects[0]
	for _, p := range chosen.projects {
		if zcodeDefault(p.Get("projectName").String()) {
			pick = p
			break
		}
	}
	id := pick.Get("projectId").String()
	if !zcodeID.MatchString(id) {
		return "", "", false
	}
	return chosen.id, id, true
}

// zcodeDefault reports the names the provider gives the account's first
// organisation and project.
func zcodeDefault(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	return strings.Contains(n, "默认机构") || strings.Contains(n, "默认项目") || n == "default"
}
