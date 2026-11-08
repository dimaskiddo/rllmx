package executor

import (
	"html"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/text"
)

// MaxErrorBody bounds how much of an error body is read.
const MaxErrorBody = 64 << 10

const maxMessage = 300

// UpstreamError is a failed answer reduced to what the pipeline may use: the
// status, a short markup-free message for the account's last-error field, and
// the time the provider says its limit ends when it says so.
type UpstreamError struct {
	Status  int
	Message string
	ResetAt time.Time
}

var (
	tagPattern   = regexp.MustCompile(`<[^>]*>`)
	spacePattern = regexp.MustCompile(`\s+`)
)

// ReadError reads an error body under the cap and parses it.
func ReadError(status int, body io.Reader) UpstreamError {
	raw, _ := io.ReadAll(io.LimitReader(body, MaxErrorBody))
	return ParseError(status, raw)
}

// ParseError takes the message from the places providers put it, in this order:
// error.message, message, an error string, an error object printed, then the
// raw text. Whatever it finds is stripped of markup and control characters and
// cut short, because the message is stored and shown on the dashboard and an
// HTML page must never reach either.
func ParseError(status int, body []byte) UpstreamError {
	e := UpstreamError{Status: status}
	msg := ""
	if gjson.ValidBytes(body) {
		g := gjson.ParseBytes(body)
		for _, p := range []string{"error.message", "message", "error"} {
			v := g.Get(p)
			if v.Type == gjson.String && v.String() != "" {
				msg = v.String()
				break
			}
		}
		if msg == "" && g.Get("error").IsObject() {
			msg = g.Get("error").Raw
		}
		e.ResetAt = resetAt(g)
	}
	if msg == "" {
		msg = string(body)
	}
	e.Message = cleanMessage(msg)
	return e
}

func cleanMessage(s string) string {
	s = tagPattern.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	s = tagPattern.ReplaceAllString(s, " ")
	s = text.StripControl(s)
	s = spacePattern.ReplaceAllString(s, " ")
	return strings.ToValidUTF8(text.CutBytes(strings.TrimSpace(s), maxMessage), "")
}

// resetAt reads a provider-reported reset: a Unix time or a number of seconds,
// in the fields the providers this program ships use.
func resetAt(g gjson.Result) time.Time {
	if v := g.Get("error.resets_at"); v.Exists() && v.Int() > 0 {
		return time.Unix(v.Int(), 0)
	}
	if v := g.Get("error.resets_in_seconds"); v.Exists() && v.Int() > 0 {
		return time.Now().Add(time.Duration(v.Int()) * time.Second)
	}
	return time.Time{}
}
