package pipeline

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"rllmx/app/stream"
	"rllmx/app/translate"
)

const (
	bypassText   = "CLI Command Execution: Clear Terminal"
	titlePhrase  = "Please write a 5-10 word title for the following conversation:"
	cliAgent     = "claude-cli"
	topicMarker  = "isNewTopic"
	contextMark  = "[1m]"
	titleWords   = 3
	settingNamed = "ccFilterNaming"
)

// matchBypass says whether a Claude CLI housekeeping request can be answered
// without an upstream, and with what text. naming is asked only when a request
// looks like a topic probe, so the settings are not read for anything else.
func matchBypass(userAgent string, req *translate.Request, naming func() bool) (string, bool) {
	if !strings.Contains(userAgent, cliAgent) || len(req.Messages) == 0 {
		return "", false
	}
	msgs := req.Messages
	last := msgs[len(msgs)-1]
	if last.Role == translate.RoleAssistant && last.Text() == "{" {
		return bypassText, true
	}
	if msgs[0].Text() == "Warmup" {
		return bypassText, true
	}
	if len(msgs) == 1 && msgs[0].Role == translate.RoleUser && msgs[0].Text() == "count" {
		return bypassText, true
	}
	var users []string
	for _, m := range msgs {
		if m.Role == translate.RoleUser {
			users = append(users, m.Text())
		}
	}
	if strings.Contains(strings.Join(users, " "), titlePhrase) {
		return bypassText, true
	}
	if system := firstSystem(msgs); strings.Contains(system, topicMarker) && naming() {
		title := ""
		if len(users) > 0 {
			f := strings.Fields(users[0])
			title = strings.Join(f[:min(titleWords, len(f))], " ")
		}
		return `{"isNewTopic":true,"title":` + strconv.Quote(title) + `}`, true
	}
	return "", false
}

func firstSystem(msgs []translate.Message) string {
	for _, m := range msgs {
		if m.Role == translate.RoleSystem {
			return m.Text()
		}
	}
	return ""
}

// bypass answers a matched probe locally and reports whether it did. The reply
// is written by the client's own codec, so it is well formed in that format.
func (c *call) bypass(ctx context.Context) (Output, bool) {
	if c.format != translate.FormatOpenAI && c.format != translate.FormatClaude {
		return Output{}, false
	}
	text, ok := matchBypass(c.in.UserAgent, c.req, func() bool { return c.namingOn(ctx) })
	if !ok {
		return Output{}, false
	}
	codec, ok := translate.Lookup(c.format)
	if !ok {
		return c.internal(ctx, errNoCodec), true
	}
	now := c.p.d.Now()
	resp := &translate.Response{
		ID:           "chatcmpl-" + strconv.FormatInt(now.UnixMilli(), 10),
		Model:        strings.TrimSuffix(c.requested, contextMark),
		Created:      now.Unix(),
		Message:      translate.Message{Role: translate.RoleAssistant, Content: translate.TextPart(text)},
		FinishReason: translate.FinishStop,
		Usage:        translate.Usage{Prompt: 1, Completion: 1},
	}
	if c.clientStream {
		return c.bypassStream(ctx, codec, resp), true
	}
	body, err := codec.WriteResponse(resp, c.tctx)
	if err != nil {
		return c.internal(ctx, err), true
	}
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	return Output{Status: http.StatusOK, Header: h, Body: body}, true
}

func (c *call) bypassStream(ctx context.Context, codec translate.Codec, resp *translate.Response) Output {
	if c.open == nil {
		return c.internal(ctx, errNoOpener)
	}
	client, err := c.open()
	if err != nil {
		return c.internal(ctx, err)
	}
	w := codec.NewStreamWriter(c.tctx)
	usage := resp.Usage
	chunks := []translate.Chunk{
		{ID: resp.ID, Model: resp.Model, Created: resp.Created, Role: translate.RoleAssistant, Text: resp.Message.Text()},
		{ID: resp.ID, Model: resp.Model, Created: resp.Created, FinishReason: resp.FinishReason, Usage: &usage},
	}
	for _, ch := range chunks {
		frames, err := w.Write(ch)
		if err != nil {
			return c.internal(ctx, err)
		}
		if !sendAll(client, frames) {
			return c.closed()
		}
	}
	if !sendAll(client, w.Close()) {
		return c.closed()
	}
	return Output{Status: http.StatusOK, Streamed: true}
}

func sendAll(client stream.Client, frames []translate.Frame) bool {
	for _, f := range frames {
		if client.Frame(f) != nil {
			return false
		}
	}
	return true
}

// namingOn reads the topic-naming setting; a failed read means off.
func (c *call) namingOn(ctx context.Context) bool {
	if c.p.d.Settings == nil {
		return false
	}
	doc, err := c.p.d.Settings.Effective(ctx)
	if err != nil {
		return false
	}
	raw, _ := doc.GetRaw(settingNamed)
	return raw == "true"
}
