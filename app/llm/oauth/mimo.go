package oauth

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/goravel/framework/contracts/log"
	"github.com/tidwall/gjson"

	"rllmx/app/llm/executor"
	"rllmx/app/llm/upstream"
	"rllmx/app/platform/cache"
	"rllmx/app/store/dialect"
)

// The account service's single sign-on host and the desktop client's own agent
// string. Neither is a secret; the cookie the exchange returns is one.
const (
	mimoAccountHost = "https://account.xiaomi.com"
	mimoSSOAgent    = "MiClaw/1.0"
	mimoCookieTTL   = 30 * time.Minute
	mimoMaxHops     = 8
)

// mimoSIDs are the service ids of the five clusters.
var mimoSIDs = map[string]string{"cn": "mimopc", "sgp": "mimosgp", "ams": "mimoams", "ru": "mimoru", "in": "mimoin"}

// MimoCookies trades an account's pass token for a service session cookie. The
// exchange follows the desktop client's own two phases; one exchange runs per
// account and cluster however many requests ask, and the cookie is kept in the
// memory-only cache for half an hour.
type MimoCookies struct {
	Tokens *TokenClient
	Cache  *cache.Cache
	Log    log.Log
}

var _ executor.CookieSource = (*MimoCookies)(nil)

// Cookie returns the cookie header value for one account. fresh skips a cached
// session, for a cached one the service ended early.
func (m *MimoCookies) Cookie(ctx context.Context, pass, userID, cUserID, region string, fresh bool) (string, error) {
	if pass == "" || len(pass) > maxMinted {
		return "", executor.ErrAccountSession
	}
	sid := mimoSIDs[strings.ToLower(region)]
	if sid == "" {
		sid = mimoSIDs["sgp"]
	}
	key := "mimo:" + dialect.HashKey(sid, pass)
	if fresh && m.Cache != nil {
		m.Cache.Delete(cache.Connections, key)
	}
	load := func(ctx context.Context) ([]byte, error) {
		cookie, err := m.exchange(ctx, pass, userID, cUserID, sid)
		if err != nil {
			return nil, err
		}
		return []byte(cookie), nil
	}
	var raw []byte
	var err error
	if m.Cache != nil {
		raw, err = m.Cache.Load(ctx, cache.Connections, key, mimoCookieTTL, load)
	} else {
		raw, err = load(ctx)
	}
	if err != nil {
		return "", executor.ErrAccountSession
	}
	return string(raw), nil
}

// exchange runs the two phases. Phase one asks for the redirect with the three
// account cookies; phase two follows that redirect without any cookie at all,
// because the service answers it with the session.
func (m *MimoCookies) exchange(ctx context.Context, pass, userID, cUserID, sid string) (string, error) {
	jar := map[string]string{}
	if userID != "" {
		jar["userId"] = userID
	}
	jar["passToken"] = pass
	if cUserID != "" {
		jar["cUserId"] = cUserID
	}
	target := mimoAccountHost + "/pass/serviceLogin?_locale=zh_CN&_snsNone=true&sid=" + url.QueryEscape(sid) + "&_json=true"
	status, raw, err := m.Tokens.get(ctx, target, map[string]string{
		"Cookie":     cookieHeader(jar),
		"User-Agent": mimoSSOAgent,
	})
	if err != nil || status != 200 {
		return "", executor.ErrAccountSession
	}
	body := strings.TrimPrefix(string(raw), "&&&START&&&")
	// The nonce is longer than a float64 keeps exactly, so the literal is signed
	// rather than a number parsed out of JSON.
	nonce := gjson.Get(body, "nonce").Raw
	if nonce == "" || gjson.Get(body, "code").Raw != "0" || gjson.Get(body, "location").String() == "" || gjson.Get(body, "ssecurity").String() == "" {
		return "", executor.ErrAccountSession
	}
	next := gjson.Get(body, "location").String()
	sep := "?"
	if strings.Contains(next, "?") {
		sep = "&"
	}
	next += sep + "clientSign=" + clientSign(nonce, gjson.Get(body, "ssecurity").String())
	for hop := 0; hop < mimoMaxHops; hop++ {
		st, hdr, err := m.Tokens.head(ctx, next, nil)
		if err != nil {
			return "", executor.ErrAccountSession
		}
		absorb(jar, hdr)
		loc := hdr.Get("Location")
		if st >= 300 && st < 400 && loc != "" {
			base, perr := url.Parse(next)
			if perr != nil {
				return "", executor.ErrAccountSession
			}
			ref, perr := url.Parse(loc)
			if perr != nil {
				return "", executor.ErrAccountSession
			}
			next = base.ResolveReference(ref).String()
			continue
		}
		break
	}
	if jar["serviceToken"] == "" && jar[sid+"_serviceToken"] == "" {
		return "", executor.ErrAccountSession
	}
	if jar["serviceToken"] == "" {
		jar["serviceToken"] = jar[sid+"_serviceToken"]
	}
	out := map[string]string{"serviceToken": jar["serviceToken"]}
	if jar["userId"] != "" {
		out["userId"] = jar["userId"]
	}
	for k, v := range jar {
		if strings.HasSuffix(k, "_ph") || strings.HasSuffix(k, "_slh") {
			out[k] = v
		}
	}
	return cookieHeader(out), nil
}

// clientSign is the reference's signature over the nonce and the secret, in
// base64 and escaped once more. The exact form is the desktop client's.
func clientSign(nonce, ssecurity string) string {
	input := "nonce=" + nonce
	if strings.TrimSpace(ssecurity) != "" {
		input += "&" + ssecurity
	}
	sum := sha1.Sum([]byte(input))
	return url.QueryEscape(base64.StdEncoding.EncodeToString(sum[:]))
}

// cookieHeader joins a jar the way a request sends it.
func cookieHeader(jar map[string]string) string {
	var b strings.Builder
	for k, v := range jar {
		if v == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("; ")
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(v)
	}
	return b.String()
}

// absorb folds a Set-Cookie header set into the jar. Only a name and a
// value are kept; a value with a separator or a control character is dropped,
// because it would change the shape of the header.
func absorb(jar map[string]string, h http.Header) {
	for _, line := range h.Values("Set-Cookie") {
		name, value, _ := strings.Cut(line, "=")
		value, _, _ = strings.Cut(value, ";")
		if !upstream.CookieName(name) || value == "" || strings.IndexFunc(value, func(r rune) bool { return r <= ' ' || r == '"' || r == ',' || r == ';' }) >= 0 {
			continue
		}
		jar[name] = value
	}
}
