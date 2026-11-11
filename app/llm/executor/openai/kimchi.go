package openai

import (
	"context"
	"regexp"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/executor"
)

// Kimchi sends to a gateway that fronts several vendors behind one OpenAI
// address. Its Anthropic-backed rows refuse the fields a Claude client adds, so
// the body is stripped to what the gateway itself reads.
type Kimchi struct {
	Default *executor.Default
}

// kimchiDrops are the top-level fields an OpenAI gateway passes on but an
// Anthropic backend rejects.
var kimchiDrops = []string{
	"anthropic_version", "anthropic_beta", "client_metadata", "mcp_servers",
	"stop_sequences", "thinking", "top_k",
}

// kimchiAnthropic matches a model id the gateway serves from an Anthropic
// backend, whose request shape rejects the reasoning fields.
var kimchiAnthropic = regexp.MustCompile(`(^|[-_/])(?:claude|anthropic)([-_/]|$)`)

// kimchiReasoning is the length above which an assistant turn's reasoning text
// is a real block rather than the placeholder a previous turn injected.
const kimchiReasoning = 8

// Execute strips the body and sends it.
func (k *Kimchi) Execute(ctx context.Context, req executor.Request) (executor.Response, error) {
	req.Body = kimchiBody(req.Body, req.Model)
	return k.Default.Execute(ctx, req)
}

// kimchiBody removes what the gateway's Anthropic-backed rows refuse: a
// top-level system field, the Claude-only keys, prompt-cache markers, and
// echoed reasoning text.
func kimchiBody(body []byte, model string) []byte {
	if !gjson.ValidBytes(body) {
		return body
	}
	body = kimchiSystem(body)
	for _, path := range kimchiDrops {
		body = executor.Remove(body, path)
	}
	if kimchiAnthropic.MatchString(model) {
		body = executor.Remove(body, "reasoning_effort")
		body = executor.Remove(body, "reasoning")
	}
	return kimchiParts(body)
}

// kimchiSystem folds a top-level system prompt into the first message, the way
// the gateway's own client does, because the field is dropped for every model.
func kimchiSystem(body []byte) []byte {
	system := gjson.GetBytes(body, "system")
	if !system.Exists() || !gjson.GetBytes(body, "messages").IsArray() {
		return body
	}
	text := kimchiText(system)
	if text != "" {
		first := gjson.GetBytes(body, "messages.0")
		joined := text
		if before := first.Get("content").String(); before != "" {
			joined = text + "\n\n" + before
		}
		body = executor.SetValue(body, "messages.0.content", joined)
	}
	return executor.Remove(body, "system")
}

// kimchiText reads a system prompt written as a string or as text parts.
func kimchiText(v gjson.Result) string {
	if v.Type == gjson.String {
		return v.String()
	}
	if !v.IsArray() {
		return ""
	}
	var parts []string
	for _, p := range v.Array() {
		if t := p.Get("text").String(); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n")
}

// kimchiParts drops the prompt-cache markers and the long reasoning text a
// Claude client leaves on a conversation's history, and keeps a short one: the
// gateway's own placeholder is a character or two, and removing it would make
// the backend ask for reasoning again.
func kimchiParts(body []byte) []byte {
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return body
	}
	for i, m := range messages.Array() {
		if reasoning := m.Get("reasoning_content"); reasoning.Type == gjson.String && len(reasoning.String()) > kimchiReasoning {
			body = executor.Remove(body, "messages."+strconv.Itoa(i)+".reasoning_content")
		}
		content := m.Get("content")
		if !content.IsArray() {
			continue
		}
		for j := range content.Array() {
			path := "messages." + strconv.Itoa(i) + ".content." + strconv.Itoa(j)
			body = executor.Remove(body, path+".cache_control")
			body = executor.Remove(body, path+".signature")
		}
	}
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return body
	}
	for i := range tools.Array() {
		body = executor.Remove(body, "tools."+strconv.Itoa(i)+".cache_control")
	}
	return body
}
