package executor

import (
	"context"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"rllmx/app/dialect"
)

// GitHub sends a request to GitHub Copilot. The credential is the minted Copilot
// token; the static identity headers come from the provider's data, and each
// request carries an id of its own. A chat body is fitted to what the endpoint
// accepts. It holds no state between requests.
type GitHub struct {
	Default *Default
}

const chatPath = "/chat/completions"

// Execute sends one call.
func (g *GitHub) Execute(ctx context.Context, req Request) (Response, error) {
	if req.Key == "" {
		return Response{}, ErrNoCredential
	}
	// The chat address takes the sanitized body and the operator's header
	// overrides; the other two take neither, as the reference does.
	if path, _, _ := strings.Cut(req.URL, "?"); strings.HasSuffix(path, chatPath) {
		req.Body = fitCopilot(req.Body)
	} else {
		req.Overrides = nil
	}
	if req.Auth != nil {
		a := *req.Auth
		a.Headers = withHeader(a.Headers, "x-request-id", dialect.NewID())
		req.Auth = &a
	}
	return g.Default.Execute(ctx, req)
}

// fitCopilot makes a chat body acceptable to the Copilot chat endpoint: message
// parts it does not know become text, empty text parts go, and an effort of
// "none" is left out because the endpoint refuses it. A body that is not an
// object is returned as it came.
func fitCopilot(body []byte) []byte {
	if !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
		return body
	}
	if gjson.GetBytes(body, "reasoning_effort").String() == "none" {
		if next, err := sjson.DeleteBytes(body, "reasoning_effort"); err == nil {
			body = next
		}
	}
	msgs := gjson.GetBytes(body, "messages")
	if !msgs.IsArray() {
		return body
	}
	var out strings.Builder
	last, changed := 0, false
	for _, m := range msgs.Array() {
		fitted, ok := fitMessage(m)
		if !ok {
			continue
		}
		changed = true
		out.WriteString(msgs.Raw[last : m.Index-msgs.Index])
		out.WriteString(fitted)
		last = m.Index - msgs.Index + len(m.Raw)
	}
	if !changed {
		return body
	}
	out.WriteString(msgs.Raw[last:])
	next, err := sjson.SetRawBytes(body, "messages", []byte(out.String()))
	if err != nil {
		return body
	}
	return next
}

// fitMessage rewrites one message whose content is an array. It reports false
// when the message needs no change.
func fitMessage(m gjson.Result) (string, bool) {
	content := m.Get("content")
	if !m.IsObject() || !content.IsArray() {
		return "", false
	}
	var parts []string
	changed := false
	for _, p := range content.Array() {
		switch p.Get("type").String() {
		case "image_url":
			parts = append(parts, p.Raw)
		case "text":
			if p.Get("text").String() == "" {
				changed = true
				continue
			}
			parts = append(parts, p.Raw)
		default:
			changed = true
			text := p.Get("text").String()
			if text == "" {
				text = p.Get("content").String()
			}
			if text == "" {
				text = p.Raw
			}
			parts = append(parts, `{"type":"text","text":`+quote(text)+`}`)
		}
	}
	if !changed {
		return "", false
	}
	value := "null"
	if len(parts) > 0 {
		value = "[" + strings.Join(parts, ",") + "]"
	}
	next, err := sjson.SetRaw(m.Raw, "content", value)
	if err != nil {
		return "", false
	}
	return next, true
}
