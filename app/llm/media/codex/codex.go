// Package codex serves Codex's own image endpoint: a plain prompt on a tool
// model, with the account's OAuth token and ChatGPT account id.
package codex

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
	"rllmx/app/llm/oauth"
	"rllmx/app/llm/upstream"
)

// toolModels are the models the native endpoint serves.
var toolModels = map[string]bool{
	"gpt-image-1.5": true, "gpt-image-2": true, "gpt-image-2.5": true,
	"gpt-image-2.5-flare": true, "gpt-image-2.5-sunburst": true,
	"gpt-image-2.6-flare": true, "gpt-image-2.6-sunburst": true,
}

// Images speaks the native generation endpoint.
type Images struct{}

// Kind is the service it serves.
func (Images) Kind() string { return upstream.KindImage }

// Build makes the call, refusing anything the native endpoint cannot honour.
func (Images) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	if !toolModels[r.WireModel] {
		return media.Call{}, media.BadRequestf("that model is not served here")
	}
	body := gjson.ParseBytes(r.Body)
	if body.Get("image").Exists() || body.Get("images.0").Exists() {
		return media.Call{}, media.BadRequestf("image editing is not served here")
	}
	if n := body.Get("n"); n.Type == gjson.Number && n.Int() != 1 {
		return media.Call{}, media.BadRequestf("this model makes one image")
	}
	if size := body.Get("size").String(); size != "" && size != "auto" {
		return media.Call{}, media.BadRequestf("this model chooses its own size")
	}
	if format := strings.ToLower(body.Get("output_format").String()); format != "" && format != "png" {
		return media.Call{}, media.BadRequestf("this model answers a png")
	}
	switch body.Get("background").String() {
	case "", "auto", "opaque":
	default:
		return media.Call{}, media.BadRequestf("this model takes no transparency")
	}
	switch q := body.Get("quality").String(); q {
	case "", "auto", "low", "medium", "high":
	default:
		return media.Call{}, media.BadRequestf("that quality is not served here")
	}
	if body.Get("output_compression").Exists() {
		return media.Call{}, media.BadRequestf("compression is not served here")
	}
	if planType(r.Credential, r.Account) == "free" {
		return media.Call{}, &media.NotEntitled{Reason: "no account entitled to image generation"}
	}
	target := "https://chatgpt.com/backend-api/codex/images/generations"
	if r.Config != nil && r.Config.BaseURL != "" {
		target = r.Config.BaseURL
	}
	req := map[string]any{
		"model": r.WireModel, "prompt": body.Get("prompt").String(), "n": 1, "size": "auto",
		"quality":       defaultValue(body.Get("quality").String(), "auto"),
		"background":    defaultValue(body.Get("background").String(), "auto"),
		"output_format": "png",
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return media.Call{}, media.BadRequestf("the request cannot be built")
	}
	account := strings.TrimSpace(r.Account["chatgptAccountId"])
	if account == "" {
		account = claimText(r.Credential, "chatgpt_account_id")
	}
	ua := "codex_cli_rs/0.159.0"
	if v := r.Entry.Transport.Headers["User-Agent"]; v != "" {
		ua = v
	}
	h := map[string]string{
		"Content-Type": "application/json", "Accept": "application/json",
		"Authorization":       "Bearer " + r.Credential,
		"originator":          "codex_cli_rs",
		"user-agent":          ua,
		"session_id":          media.UUID(),
		"x-client-request-id": media.UUID(),
	}
	if account != "" {
		h["chatgpt-account-id"] = account
	}
	return media.Call{Method: http.MethodPost, URL: target, Header: h, Body: raw}, nil
}

// Parse reads the generated image and its usage.
func (Images) Parse(_ context.Context, _ media.Request, _ media.Client, status int, header http.Header, body []byte) (media.Response, error) {
	if status < 200 || status > 299 {
		return media.Response{}, &media.ProviderError{Status: status, Text: "the provider refused the image"}
	}
	if !gjson.ValidBytes(body) {
		return media.Response{}, &media.ProviderError{Status: http.StatusBadGateway, Text: "the provider answered no image"}
	}
	data := gjson.GetBytes(body, "data")
	if !data.IsArray() || len(data.Array()) == 0 {
		return media.Response{}, &media.ProviderError{Status: http.StatusBadGateway, Text: "the provider answered no image"}
	}
	out := []map[string]string{}
	for _, item := range data.Array() {
		if b := item.Get("b64_json").String(); b != "" {
			out = append(out, map[string]string{"b64_json": b})
		} else if u := item.Get("url").String(); u != "" {
			out = append(out, map[string]string{"url": u})
		}
	}
	if len(out) == 0 {
		return media.Response{}, &media.ProviderError{Status: http.StatusBadGateway, Text: "the provider answered no image"}
	}
	payload := map[string]any{"created": gjson.GetBytes(body, "created").Int(), "data": out}
	raw, _ := json.Marshal(payload)
	u := media.Usage{}
	in, o := gjson.GetBytes(body, "usage.input_tokens"), gjson.GetBytes(body, "usage.output_tokens")
	if in.Type == gjson.Number && o.Type == gjson.Number {
		u = media.Usage{Prompt: in.Int(), Completion: o.Int(), Known: true}
	}
	return media.Response{Status: http.StatusOK, Header: map[string]string{"Content-Type": "application/json"}, Body: raw, Usage: u}, nil
}

// planType reads the ChatGPT plan from the token's own claims, then the stored
// value: a plan changes while a stored one goes stale.
func planType(access string, account map[string]string) string {
	if p := claimText(access, "chatgpt_plan_type"); p != "" {
		return p
	}
	return strings.ToLower(strings.TrimSpace(account["chatgptPlanType"]))
}

// claimText reads one claim of a token's OpenAI auth block. gjson treats a dot
// as a path separator, so the claim's own dots are escaped.
func claimText(token, name string) string {
	c, ok := oauth.Claims(token)
	if !ok {
		return ""
	}
	return c.Get(`https://api\.openai\.com/auth.` + name).String()
}

func defaultValue(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
