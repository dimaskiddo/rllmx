package executor

import (
	"context"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"rllmx/app/dialect"
)

const (
	// scrubLength is the length above which a system prompt is taken for an agent's.
	scrubLength     = 2000
	neutralPrompt   = "You are a helpful AI assistant that helps with software engineering tasks."
	codebuddyPrompt = "You are CodeBuddy Code."
	// frequencyCode is the gateway's code for a rate limit.
	frequencyCode = 6004
	// defaultZone is the offset of a limit message that names none: the gateway
	// writes China time.
	defaultZone   = 8 * time.Hour
	maxZoneHours  = 14
	maxLimitReset = 400 * 24 * time.Hour
)

var (
	// agentPrompt matches the identity lines coding agents put in a system prompt,
	// which the CN gateway's content filter rejects as an injection.
	agentPrompt = regexp.MustCompile(`(?i)you are claude code|claude.?code.+official.+cli|anthropic.+official.+cli|you are (?:cursor|windsurf|cline|aider|continue|copilot|cody)|you are an? (?:ai )?(?:coding |code )?agent|cc_entrypoint\s*=\s*(?:cli|vscode|jetbrains|gui)|claude.?code.+issues|give feedback.+claude.?code|you are .{0,30}(?:powerful )?ai agent|orchestration capabilities|ohmyopencode|<agent-identity>|<role>|<behavior_instructions>`)
	// limitMessage matches the words of a rate-limit refusal.
	limitMessage = regexp.MustCompile(`(?i)超出频率限制|frequency limit|限额`)
	// limitDate finds the moment a limit ends in the message, with an optional zone.
	limitDate = regexp.MustCompile(`(\d{4}-\d{2}-\d{2})\s+(\d{2}:\d{2}:\d{2})(?:\s*UTC\+?([0-9:]+))?`)
)

// CodeBuddy sends a request to one of the two CodeBuddy gateways. They take an
// OpenAI chat body, but each wants it dressed its own way, and both refuse a
// request that is not a stream (the data forces one). It holds no state.
type CodeBuddy struct {
	Default *Default
	// Intl is the international gateway, which rebuilds the messages; the China
	// gateway instead replaces an agent's system prompt.
	Intl bool
	// Now is the clock for a limit's reset time; nil uses time.Now.
	Now func() time.Time
}

// Execute sends one call.
func (c *CodeBuddy) Execute(ctx context.Context, req Request) (Response, error) {
	if req.Key == "" {
		return Response{}, ErrNoCredential
	}
	req.Body = c.shape(req.Body)
	if req.Auth != nil {
		id := dialect.NewID()
		a := *req.Auth
		a.Headers = withHeaders(a.Headers, map[string]string{
			"X-Request-ID": id, "X-Conversation-ID": dialect.NewID(),
			"X-Conversation-Request-ID": id, "X-Conversation-Message-ID": id,
		})
		req.Auth = &a
	}
	resp, err := c.Default.Execute(ctx, req)
	if err != nil || resp.Status < http.StatusBadRequest {
		return resp, err
	}
	raw := failedBody(resp)
	if out, ok := c.limited(raw); ok {
		return replyWith(resp, http.StatusTooManyRequests, out, "application/json"), nil
	}
	return replyWith(resp, resp.Status, raw, ""), nil
}

// shape dresses the body for the gateway: the reasoning fields every gateway
// reads, then the messages.
func (c *CodeBuddy) shape(body []byte) []byte {
	if !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
		return body
	}
	switch gjson.GetBytes(body, "reasoning_effort").String() {
	case "":
	case "none", "off":
		body, _ = sjson.DeleteBytes(body, "reasoning_effort")
	default:
		body, _ = sjson.SetBytes(body, "reasoning_summary", "auto")
	}
	if c.Intl {
		return rebuildMessages(body)
	}
	return scrubSystem(body)
}

// scrubSystem replaces a system prompt that is long or names a coding agent with
// a neutral one. A prompt of the user's own is left alone.
func scrubSystem(body []byte) []byte {
	msgs := gjson.GetBytes(body, "messages")
	if !msgs.IsArray() {
		return body
	}
	for i, m := range msgs.Array() {
		if m.Get("role").String() != "system" {
			continue
		}
		flat := flatText(m.Get("content"))
		if flat == "" || (utf8.RuneCountInString(flat) <= scrubLength && !agentPrompt.MatchString(flat)) {
			continue
		}
		path := "messages." + strconv.Itoa(i) + ".content"
		if m.Get("content").Type == gjson.String {
			body, _ = sjson.SetBytes(body, path, neutralPrompt)
		} else {
			body, _ = sjson.SetRawBytes(body, path, []byte(`[{"type":"text","text":`+quoteJSON(neutralPrompt)+`}]`))
		}
	}
	return body
}

// flatText is a message's text whether it is a string or typed blocks.
func flatText(content gjson.Result) string {
	if content.Type == gjson.String {
		return content.String()
	}
	if !content.IsArray() {
		return ""
	}
	parts := make([]string, 0, 4)
	for _, b := range content.Array() {
		parts = append(parts, b.Get("text").String())
	}
	return strings.Join(parts, "\n")
}

// rebuildMessages gives the conversation the shape the international gateway
// accepts: its own system prompt first, no other system or developer turn, and a
// user string as a typed block.
func rebuildMessages(body []byte) []byte {
	msgs := gjson.GetBytes(body, "messages")
	var out strings.Builder
	out.WriteString(`[{"role":"system","content":` + quoteJSON(codebuddyPrompt) + `}`)
	if msgs.IsArray() {
		for _, m := range msgs.Array() {
			if !m.IsObject() {
				continue
			}
			switch m.Get("role").String() {
			case "system", "developer":
				continue
			case "user":
				if c := m.Get("content"); c.Type == gjson.String {
					raw, err := sjson.SetRawBytes([]byte(m.Raw), "content", []byte(`[{"type":"text","text":`+quoteJSON(c.String())+`}]`))
					if err == nil {
						out.WriteByte(',')
						out.Write(raw)
						continue
					}
				}
			}
			out.WriteByte(',')
			out.WriteString(m.Raw)
		}
	}
	out.WriteByte(']')
	next, err := sjson.SetRawBytes(body, "messages", []byte(out.String()))
	if err != nil {
		return body
	}
	return next
}

// limited reads a rate-limit refusal: the code or the words of the message. It
// returns the 429 body the pipeline reads, with the moment the limit ends when
// the message names one. A body that is not a limit is not touched.
func (c *CodeBuddy) limited(raw []byte) ([]byte, bool) {
	if !gjson.ValidBytes(raw) {
		return nil, false
	}
	g := gjson.ParseBytes(raw)
	msg := firstText(g.Get("msg"), g.Get("message"), g.Get("error.message"))
	if g.Get("code").Int() != frequencyCode && !limitMessage.MatchString(msg) {
		return nil, false
	}
	if msg == "" {
		msg = "CodeBuddy frequency limit (6004)"
	}
	out, _ := sjson.SetBytes([]byte(`{"error":{}}`), "error.message", msg)
	if at, ok := limitEnd(msg); ok && at.After(c.now()) && at.Before(c.now().Add(maxLimitReset)) {
		out, _ = sjson.SetBytes(out, "error.resets_at", at.Unix())
	}
	return out, true
}

func (c *CodeBuddy) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// limitEnd reads the date a limit message gives, in the zone it names or China
// time.
func limitEnd(msg string) (time.Time, bool) {
	m := limitDate.FindStringSubmatch(msg)
	if m == nil {
		return time.Time{}, false
	}
	zone := defaultZone
	if m[3] != "" {
		z, ok := zoneOffset(m[3])
		if !ok {
			return time.Time{}, false
		}
		zone = z
	}
	t, err := time.ParseInLocation("2006-01-02 15:04:05", m[1]+" "+m[2], time.FixedZone("", int(zone/time.Second)))
	return t, err == nil
}

// zoneOffset reads "8", "08" or "8:30" as a positive offset east of UTC.
func zoneOffset(s string) (time.Duration, bool) {
	h, mm, _ := strings.Cut(s, ":")
	hours, err := strconv.Atoi(h)
	if err != nil || hours < 0 || hours > maxZoneHours {
		return 0, false
	}
	mins := 0
	if mm != "" {
		if mins, err = strconv.Atoi(mm); err != nil || mins < 0 || mins > 59 {
			return 0, false
		}
	}
	return time.Duration(hours)*time.Hour + time.Duration(mins)*time.Minute, true
}

// quoteJSON renders a string as a JSON string literal.
func quoteJSON(s string) string {
	raw, _ := sjson.SetBytes([]byte(`{}`), "v", s)
	return gjson.GetBytes(raw, "v").Raw
}

// withHeaders copies h and sets each given header.
func withHeaders(h, add map[string]string) map[string]string {
	out := make(map[string]string, len(h)+len(add))
	for k, v := range h {
		out[k] = v
	}
	for k, v := range add {
		out[k] = v
	}
	return out
}
