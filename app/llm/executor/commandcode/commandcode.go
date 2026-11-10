package commandcode

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/executor"
	"rllmx/app/llm/upstream"
	"rllmx/app/platform/text"
)

// commandCodeAttempts is how many times a refusal that another try may clear is
// sent again, and the wait before each.
const (
	commandCodeAttempts = 3
	commandCodeBackoff  = time.Second
)

// commandCodeProject is the workspace name the upstream expects. The reference
// slugifies the directory it happens to run in; a fixed word keeps the host's
// layout off the wire.
const commandCodeProject = "workspace"

// CommandCode sends to the agent gateway, whose answer is a stream of AI SDK
// events. An error the upstream reports inside a 200 is turned back into the
// status it stands for, so the caller sees a real refusal rather than a stream
// that carries none.
type CommandCode struct {
	Default *executor.Default
	// PeekIdle ends the look at the start of an answer when no byte arrives for
	// this long; zero waits as long as the answer takes.
	PeekIdle time.Duration
}

// Execute sends one call, retrying a busy or unavailable answer twice.
func (c *CommandCode) Execute(ctx context.Context, req executor.Request) (executor.Response, error) {
	if req.Key == "" {
		return executor.Response{}, executor.ErrNoCredential
	}
	// The upstream streams only, so a client that asked for JSON has its answer
	// collected from the stream.
	req.Stream = true
	thread := gjson.GetBytes(req.Body, "threadId").String()
	if thread == "" {
		return executor.Response{}, executor.ErrBadAccount
	}
	req.Auth = &upstream.Auth{Scheme: upstream.SchemeBearer, Header: "Authorization", Headers: map[string]string{
		"x-session-id":   thread,
		"x-project-slug": commandCodeProject,
		"Accept":         "text/event-stream",
	}}
	for attempt := 1; ; attempt++ {
		resp, err := c.Default.Send(ctx, req)
		if err != nil {
			return executor.Response{}, err
		}
		if resp.Status < 200 || resp.Status > 299 {
			if attempt < commandCodeAttempts && retryableStatus(resp.Status) {
				executor.Drain(resp.Body)
				if werr := executor.Wait(ctx, commandCodeBackoff*time.Duration(attempt), c.Default.Sleep); werr != nil {
					return executor.Response{}, werr
				}
				continue
			}
			return resp, nil
		}
		status, body := commandCodePeek(ctx, resp.Body, c.PeekIdle)
		if status != 0 {
			if attempt < commandCodeAttempts && retryableStatus(status) {
				if werr := executor.Wait(ctx, commandCodeBackoff*time.Duration(attempt), c.Default.Sleep); werr != nil {
					return executor.Response{}, werr
				}
				continue
			}
			return executor.Response{Status: status, Header: resp.Header, Body: http.NoBody}, nil
		}
		resp.Body = body
		resp.Header = executor.StreamHeader(resp.Header)
		return resp, nil
	}
}

// retryableStatus is an answer another attempt may clear.
func retryableStatus(status int) bool {
	return status == http.StatusBadGateway || status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout
}

// commandCodePeek reads the answer up to its first real event. An error event
// ends the call with the status it stands for, and nothing is replayed; anything
// else yields a body that gives back every byte.
func commandCodePeek(ctx context.Context, body io.ReadCloser, idle time.Duration) (int, io.ReadCloser) {
	status, rest, _ := executor.PeekUntil(ctx, body, idle, func(seen []byte) (int, bool, bool) {
		if st := commandCodeErrorStatus(seen); st != 0 {
			return st, true, false
		}
		return 0, false, commandCodeHasEvent(seen)
	})
	return status, rest
}

// commandCodeErrorStatus is the status an error event in the first lines stands
// for, or 0 while none has arrived. An event that is split across two reads is
// read once it is whole.
func commandCodeErrorStatus(seen []byte) int {
	for _, line := range strings.Split(string(seen), "\n") {
		line, _ = text.DataLine(line)
		if line == "" || !gjson.Valid(line) {
			continue
		}
		if gjson.Get(line, "type").String() != "error" {
			continue
		}
		return commandCodeStatus(line)
	}
	return 0
}

// commandCodeHasEvent reports whether a line that a client can act on has
// arrived, which is where the look stops.
func commandCodeHasEvent(seen []byte) bool {
	for _, line := range strings.Split(string(seen), "\n") {
		line, _ = text.DataLine(line)
		if !gjson.Valid(line) {
			continue
		}
		switch gjson.Get(line, "type").String() {
		case "text-delta", "reasoning-delta", "tool-input-start", "tool-call", "finish", "finish-step":
			return true
		}
	}
	return false
}

// commandCodeStatus reads the status an error event names, or the one its words
// stand for. The reference's own classification, minus its reading of the
// provider's text: only the codes it sends are trusted, and a message is
// matched by its kind, never repeated.
func commandCodeStatus(line string) int {
	g := gjson.Parse(line)
	for _, p := range []string{"error.statusCode", "error.status", "statusCode", "code"} {
		if v := int(g.Get(p).Int()); v >= 400 && v <= 599 {
			return v
		}
	}
	text := strings.ToLower(g.Get("error.message").String() + " " + g.Get("error").String() + " " + g.Get("message").String())
	switch {
	case strings.Contains(text, "rate limit"), strings.Contains(text, "too many requests"):
		return http.StatusTooManyRequests
	case strings.Contains(text, "unauthorized"), strings.Contains(text, "invalid api key"), strings.Contains(text, "authentication"):
		return http.StatusUnauthorized
	case strings.Contains(text, "payment required"), strings.Contains(text, "billing"):
		return http.StatusPaymentRequired
	case strings.Contains(text, "quota"), strings.Contains(text, "forbidden"), strings.Contains(text, "permission"):
		return http.StatusForbidden
	case strings.Contains(text, "not found"):
		return http.StatusNotFound
	}
	return http.StatusServiceUnavailable
}
