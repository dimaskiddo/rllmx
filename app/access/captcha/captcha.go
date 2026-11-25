// Package captcha verifies the challenge a sign-in page asks a remote caller to
// pass: Cloudflare Turnstile or Google reCAPTCHA v2. The page's script and the
// check request are the only things here that leave this process.
package captcha

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/tidwall/gjson"
)

// The providers a sign-in may be asked to pass.
const (
	Turnstile = "turnstile"
	Recaptcha = "recaptcha"
)

// The reasons a check can fail. The caller tells a refused answer, which is the
// caller's fault, from an unreachable provider, which is not.
var (
	ErrMissing     = errors.New("captcha: no answer was sent")
	ErrRejected    = errors.New("captcha: the provider refused the answer")
	ErrUnavailable = errors.New("captcha: the provider could not be reached")
)

const (
	maxToken = 8 << 10
	maxReply = 64 << 10
	// onload names the function the provider's script calls once its own
	// interface is ready; the page defines it, the script only names it.
	onload = "rllmCaptchaReady"
)

// verifyURL is where an answer is checked, per provider.
var verifyURL = map[string]string{
	Turnstile: "https://challenges.cloudflare.com/turnstile/v0/siteverify",
	Recaptcha: "https://www.google.com/recaptcha/api/siteverify",
}

// Verifier checks an answer with the provider that issued it.
type Verifier struct {
	Provider string
	Secret   string
	// URL is the check address; New fills it in from the provider.
	URL string
	// Client reaches the provider through the SSRF guard. A nil client uses the
	// default one.
	Client *http.Client
}

// New builds a verifier for a provider. An unknown provider returns nil, which
// the sign-in reads as "ask nothing".
func New(provider, secret string, client *http.Client) *Verifier {
	if provider == "" {
		return nil
	}
	return &Verifier{Provider: provider, Secret: secret, URL: verifyURL[provider], Client: client}
}

// Verify checks one answer. remoteIP is the address the provider is told the
// answer came from; an empty one is left out.
func (v *Verifier) Verify(ctx context.Context, token, remoteIP string) error {
	token = strings.TrimSpace(token)
	if v == nil || v.URL == "" {
		return ErrUnavailable
	}
	if token == "" || len(token) > maxToken {
		return ErrMissing
	}
	form := url.Values{"secret": {v.Secret}, "response": {token}}
	if remoteIP != "" {
		form.Set("remoteip", remoteIP)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.URL, strings.NewReader(form.Encode()))
	if err != nil {
		return ErrUnavailable
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := v.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return ErrUnavailable
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxReply))
	if err != nil || resp.StatusCode < 200 || resp.StatusCode > 299 {
		return ErrUnavailable
	}
	if !gjson.GetBytes(body, "success").Bool() {
		return ErrRejected
	}
	return nil
}

// Script is the provider's loader, asked for the explicit render so the page
// decides where the widget goes.
func Script(provider string) string {
	switch provider {
	case Turnstile:
		return "https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit&onload=" + onload
	case Recaptcha:
		return "https://www.google.com/recaptcha/api.js?render=explicit&onload=" + onload
	}
	return ""
}

// Sources are the hosts a page must allow to load the provider's loader and to
// hold its challenge frame. Each host list is a whole CSP source expression
// (paths allowed); the caller adds them to one directive.
func Sources(provider string) (script, frame []string) {
	switch provider {
	case Turnstile:
		// Cloudflare's CSP page: one host for both.
		return []string{"https://challenges.cloudflare.com"}, []string{"https://challenges.cloudflare.com"}
	case Recaptcha:
		// Google's reCAPTCHA CSP FAQ.
		return []string{"https://www.google.com/recaptcha/", "https://www.gstatic.com/recaptcha/"},
			[]string{"https://www.google.com/recaptcha/", "https://recaptcha.google.com/recaptcha/"}
	}
	return nil, nil
}

// Policy is the page CSP a sign-in page needs while a provider is configured:
// the given base policy with the provider's hosts added to script-src and
// frame-src. An unknown or empty provider returns the base policy unchanged.
func Policy(base, provider string) string {
	script, frame := Sources(provider)
	if len(script) == 0 || len(frame) == 0 {
		return base
	}
	return fmt.Sprintf("%s; script-src 'self' %s; frame-src %s",
		base, strings.Join(script, " "), strings.Join(frame, " "))
}
