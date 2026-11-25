package controllers

import (
	"context"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
)

// webProbe is how one web-session provider answers whether a session is signed
// in. A probe that cannot run answers unknown, which keeps the account active
// with a warning rather than marking a working login broken.
type webProbe struct {
	url    string
	accept string
	// cookie names the cookie the session must carry; empty means the pasted
	// value is a token, not a cookie header.
	cookie string
	decide func(status int, body []byte) Result
}

var webProbes = map[string]webProbe{
	"chatgpt-web": {
		url: "https://chatgpt.com/api/auth/session", accept: "application/json", cookie: "__Secure-next-auth.session-token",
		decide: func(status int, body []byte) Result { return jsonSession(status, body, "user.email") },
	},
	"uc-web": {
		url: "https://uncensored.com/", accept: "text/html", cookie: "__client",
		// The session is checked by the same client the chat path uses.
	},
	"gemini-web": {
		url: "https://gemini.google.com/app", accept: "text/html,application/xhtml+xml", cookie: "__Secure-1PSID",
		decide: func(status int, body []byte) Result {
			if status != http.StatusOK {
				return Result{Outcome: OutcomeUnexpected}
			}
			if !strings.Contains(string(body), `"SNlM0e":`) {
				return Result{Outcome: OutcomeInvalidKey}
			}
			return Result{Outcome: OutcomeValid}
		},
	},
	"muse-web": {
		url: "https://muse.ai/api/session", accept: "application/json", cookie: "hatch_sess",
		decide: func(status int, body []byte) Result {
			if status/100 == 2 {
				return Result{Outcome: OutcomeValid}
			}
			return Result{Outcome: OutcomeInvalidKey}
		},
	},
	"grok-web": {
		url: "https://grok.com/api/auth/session", accept: "application/json", cookie: "sso",
		decide: func(status int, body []byte) Result {
			if status != http.StatusOK || gjson.GetBytes(body, "status").String() != "authenticated" {
				return Result{Outcome: OutcomeInvalidKey}
			}
			return Result{Outcome: OutcomeValid}
		},
	},
}

// CheckWeb asks a web-session provider whether the stored session is signed in.
// A provider this build cannot probe answers unknown: active, with a warning.
func (c *Checker) CheckWeb(ctx context.Context, provider, raw string) Result {
	probe, ok := webProbes[provider]
	if !ok {
		return Result{Outcome: OutcomeValid, Warning: true}
	}
	if probe.cookie == "" {
		return Result{Outcome: OutcomeValid, Warning: true}
	}
	jar, err := media.ParseCookie(raw)
	if err != nil {
		return Result{Outcome: OutcomeInvalidKey}
	}
	if jar.Get(probe.cookie) == "" {
		return Result{Outcome: OutcomeInvalidKey}
	}
	if provider == "uc-web" {
		return c.checkUCWeb(ctx, jar)
	}
	status, body, out := c.doAccept(ctx, http.MethodGet, probe.url, probe.accept,
		map[string]string{"Cookie": jar.Header(), "User-Agent": media.WebUserAgent}, nil)
	switch out {
	case OutcomeNoConnect, OutcomeTimedOut, OutcomeCertificate, OutcomeRedirected:
		// The account is kept: a network fault is not a refused session.
		return Result{Outcome: OutcomeValid, Warning: true}
	case OutcomeTooLarge:
		return Result{Outcome: OutcomeValid, Warning: true}
	case OutcomeValid:
	default:
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			return Result{Outcome: OutcomeInvalidKey}
		}
		return Result{Outcome: out}
	}
	return probe.decide(status, body)
}

// checkUCWeb runs the two clerk calls the client makes before its chat: the
// client record names the active session, and a token for it proves it.
func (c *Checker) checkUCWeb(ctx context.Context, jar *media.CookieJar) Result {
	headers := map[string]string{
		"Cookie":     "__client=" + jar.Get("__client"),
		"User-Agent": media.WebUserAgent,
		"Origin":     "https://uncensored.com",
		"Referer":    "https://uncensored.com/",
		"Accept":     "application/json",
	}
	status, body, out := c.doAccept(ctx, http.MethodGet,
		"https://clerk.uncensored.com/v1/client?_clerk_js_version=5.127.1", "application/json", headers, nil)
	if out != OutcomeValid || status != http.StatusOK {
		return unknownOrInvalid(status, out)
	}
	sid := ""
	for _, s := range gjson.GetBytes(body, "response.sessions").Array() {
		if s.Get("status").String() == "active" {
			sid = s.Get("id").String()
			break
		}
	}
	if sid == "" {
		return Result{Outcome: OutcomeInvalidKey}
	}
	headers["Content-Type"] = "application/x-www-form-urlencoded"
	status, body, out = c.doAccept(ctx, http.MethodPost,
		"https://clerk.uncensored.com/v1/client/sessions/"+sid+"/tokens?_clerk_js_version=5.127.1",
		"application/json", headers, []byte{})
	if out != OutcomeValid || status != http.StatusOK {
		return unknownOrInvalid(status, out)
	}
	if gjson.GetBytes(body, "jwt").String() == "" {
		return Result{Outcome: OutcomeInvalidKey}
	}
	return Result{Outcome: OutcomeValid}
}

// unknownOrInvalid keeps an account on a network fault and refuses a refusal.
func unknownOrInvalid(status int, out Outcome) Result {
	switch {
	case out == OutcomeValid && (status == http.StatusUnauthorized || status == http.StatusForbidden):
		return Result{Outcome: OutcomeInvalidKey}
	case out == OutcomeValid:
		return Result{Outcome: OutcomeUnexpected}
	}
	return Result{Outcome: OutcomeValid, Warning: true}
}

// jsonSession reads one field from a session answer: a value means signed in.
func jsonSession(status int, body []byte, field string) Result {
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return Result{Outcome: OutcomeInvalidKey}
	}
	if status != http.StatusOK {
		return Result{Outcome: OutcomeUnexpected}
	}
	if gjson.GetBytes(body, field).String() == "" {
		return Result{Outcome: OutcomeInvalidKey}
	}
	return Result{Outcome: OutcomeValid}
}

// CheckDeepSeekWeb judges the pasted web token the deepseek-web provider sends.
func (c *Checker) CheckDeepSeekWeb(ctx context.Context, token string) Result {
	token = strings.TrimSpace(token)
	if token == "" {
		return Result{Outcome: OutcomeInvalidKey}
	}
	if strings.HasPrefix(token, "{") {
		token = gjson.Get(token, "value").String()
	}
	status, body, out := c.doAccept(ctx, http.MethodGet, "https://chat.deepseek.com/api/v0/users/current", "application/json", map[string]string{
		"Authorization":      "Bearer " + token,
		"X-Client-Platform":  "web",
		"X-Client-Bundle-Id": "com.deepseek.chat",
		"X-Client-Version":   "2.0.0",
		"User-Agent":         media.WebUserAgent,
	}, nil)
	switch out {
	case OutcomeValid:
	case OutcomeNoConnect, OutcomeTimedOut, OutcomeCertificate, OutcomeRedirected, OutcomeTooLarge:
		return Result{Outcome: OutcomeValid, Warning: true}
	default:
		return Result{Outcome: out}
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return Result{Outcome: OutcomeInvalidKey}
	}
	if status != http.StatusOK || gjson.GetBytes(body, "code").Int() == 40003 {
		return Result{Outcome: OutcomeInvalidKey}
	}
	if gjson.GetBytes(body, "data.biz_data.token").String() == "" {
		return Result{Outcome: OutcomeInvalidKey}
	}
	return Result{Outcome: OutcomeValid}
}
