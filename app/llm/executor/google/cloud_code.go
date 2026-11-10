package google

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"rllmx/app/llm/executor"
	"rllmx/app/llm/upstream"
	"rllmx/app/store/dialect"
	"rllmx/app/store/document"
)

// imageModel names the models that make images, which Google answers only
// without a stream.
var imageModel = regexp.MustCompile(`(?i)image|imagen`)

// cliAgentHeader is the data key that holds the Gemini CLI user agent template.
// It is a name no request ever carries, so the common header merge cannot send
// the template itself.
const cliAgentHeader = "X-Rllmx-User-Agent"

const (
	// retryCeiling is the longest wait an in-place retry accepts: a longer reset
	// goes to the next account instead.
	retryCeiling = 10 * time.Second
	// transientCeiling caps the backoff for a failure that carries no reset.
	transientCeiling = 15 * time.Second
	retryTries       = 3
)

// transientWords are the words Antigravity's backend uses for a failure worth
// trying again.
var transientWords = regexp.MustCompile(`(?i)high\s+traffic|agent\s+(execution\s+)?terminated\s+due\s+to\s+error|capacity|temporarily\s+unavailable|timeout|stream\s+(ended|closed|terminated|interrupted)|empty\s+response`)

// resetAfter reads "reset after 2h7m23s" in a refusal's text.
var resetAfter = regexp.MustCompile(`(?i)reset after (\d+h)?(\d+m)?(\d+s)?`)

// CloudCode sends a request to Google's Cloud Code API, as Gemini CLI or as
// Antigravity. The body arrives wrapped by the codec with its model and no
// project; this fills in the account's project, the session and the request id,
// picks the address and shapes the headers. It holds no state.
type CloudCode struct {
	Default *executor.Default
	// Antigravity selects the Antigravity client; false is Gemini CLI.
	Antigravity bool
	Now         func() time.Time
}

func (c *CloudCode) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Execute sends one call.
func (c *CloudCode) Execute(ctx context.Context, req executor.Request) (executor.Response, error) {
	if req.Key == "" {
		return executor.Response{}, executor.ErrNoCredential
	}
	project := ""
	if req.Account != nil {
		project = req.Account("projectId")
	}
	if !upstream.ValidProject(project) {
		return executor.Response{}, executor.ErrBadAccount
	}
	image := c.Antigravity && imageModel.MatchString(req.Model)
	stream := req.Stream && !image
	req.Stream = stream
	req.URL = cloudCodeURL(req.URL, stream)
	req.Body = c.fill(req, project)
	if req.Auth != nil {
		a := *req.Auth
		a.Headers = executor.WithHeaders(a.Headers, c.headers(req))
		req.Auth = &a
	}
	resp, err := c.attempt(ctx, req)
	if err != nil || resp.Status < http.StatusBadRequest {
		return resp, err
	}
	raw := executor.FailedBody(resp)
	if out, ok := c.limited(resp.Status, resp.Header, raw); ok {
		return executor.ReplyWith(resp, resp.Status, out, "application/json"), nil
	}
	return executor.ReplyWith(resp, resp.Status, raw, ""), nil
}

// attempt sends the request and, for Antigravity, tries again in place on the
// statuses the provider asks for, for as long as its own reset hint allows.
func (c *CloudCode) attempt(ctx context.Context, req executor.Request) (executor.Response, error) {
	tries := 0
	// The in-place retry of 429, 500 and 503 is this executor's own, with delays
	// from the provider's hints, so the common table must not retry them as well.
	d := *c.Default
	if c.Antigravity {
		d.Retry = map[int]executor.Rule{}
	}
	for {
		resp, err := d.Execute(ctx, req)
		if err != nil || !c.Antigravity || tries >= retryTries || !antigravityRetryable(resp.Status) {
			return resp, err
		}
		raw := executor.FailedBody(resp)
		delay, ok := antigravityDelay(resp.Header, raw, resp.Status, tries, c.now())
		if !ok {
			return executor.ReplyWith(resp, resp.Status, raw, ""), nil
		}
		tries++
		if werr := executor.Wait(ctx, delay, c.Default.Sleep); werr != nil {
			return executor.Response{}, werr
		}
	}
}

func antigravityRetryable(status int) bool {
	return status == http.StatusTooManyRequests || status == http.StatusInternalServerError || status == http.StatusServiceUnavailable
}

// cloudCodeURL turns the base address into the action's address.
func cloudCodeURL(base string, stream bool) string {
	base = strings.TrimRight(base, "/")
	if stream {
		return base + ":streamGenerateContent?alt=sse"
	}
	return base + ":generateContent"
}

// fill puts the account's project, the session and the request id into the
// envelope.
func (c *CloudCode) fill(req executor.Request, project string) []byte {
	body := req.Body
	if !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
		return body
	}
	body, _ = sjson.SetBytes(body, "project", project)
	if c.Antigravity {
		session := req.Session
		if session == "" {
			session = "anonymous"
		}
		step := int64(max(1, 2*int(gjson.GetBytes(body, "request.contents.#").Int())-1))
		body, _ = sjson.SetBytes(body, "requestId", antigravityRequestID(session, req.Model, step, c.now()))
		body, _ = sjson.SetBytes(body, "request.sessionId", numericSession(session))
	} else {
		body, _ = sjson.SetBytes(body, "requestId", "agent-"+dialect.NewID())
		body, _ = sjson.SetBytes(body, "request.sessionId", numericSession(cmp.Or(req.Session, dialect.NewID())))
	}
	return body
}

// numericSession turns a session id into the "-<int64>" form the clients send.
func numericSession(id string) string {
	sum := sha256.Sum256([]byte(id))
	var n uint64
	for _, b := range sum[:8] {
		n = n<<8 | uint64(b)
	}
	return "-" + strconv.FormatUint(n&0x7fffffffffffffff, 10)
}

// seededUUID makes a version 5 shaped UUID from a seed, so one conversation keeps
// one id.
func seededUUID(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	b := sum[:16]
	b[6] = b[6]&0x0f | 0x50
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// antigravityRequestID is `agent/<conversation>/<ms>/<trajectory>/<step>`.
func antigravityRequestID(session, model string, step int64, now time.Time) string {
	return "agent/" + seededUUID("antigravity:conversation:"+session) + "/" + strconv.FormatInt(now.UnixMilli(), 10) +
		"/" + seededUUID("antigravity:trajectory:"+session+":"+model+":agent") + "/" + strconv.FormatInt(step, 10)
}

// headers are the identity headers of the client, with fixed words where the
// real client would send the host's own details.
func (c *CloudCode) headers(req executor.Request) map[string]string {
	h := map[string]string{"Accept": "application/json"}
	if req.Stream {
		h["Accept"] = "text/event-stream"
	}
	if !c.Antigravity {
		// The version and the client string come from the provider's data; the
		// model is the request's. The host's own system and architecture are
		// never sent.
		h["User-Agent"] = geminiCLIAgent(req.Entry.Transport.Headers[cliAgentHeader], req.Model)
	}
	return h
}

// limited reads a Google refusal's retry hint and writes it where the lock
// reporter looks: error.resets_at. Gemini CLI reads RetryInfo on a 429;
// Antigravity reads the quota reset stamp or retry delay on a 429 or 409.
func (c *CloudCode) limited(status int, h http.Header, raw []byte) ([]byte, bool) {
	if status != http.StatusTooManyRequests && !(c.Antigravity && status == http.StatusConflict) {
		return nil, false
	}
	at, ok := googleReset(raw, c.Antigravity, c.now())
	if !ok {
		return nil, false
	}
	msg := document.FirstString(gjson.GetBytes(raw, "error.message"), gjson.GetBytes(raw, "message"))
	out, _ := sjson.SetBytes([]byte(`{"error":{}}`), "error.message", msg)
	out, _ = sjson.SetBytes(out, "error.resets_at", at.Unix())
	return out, true
}

// googleReset finds the moment a limit ends in a Google error body.
func googleReset(raw []byte, antigravity bool, now time.Time) (time.Time, bool) {
	if !gjson.ValidBytes(raw) {
		return time.Time{}, false
	}
	for _, d := range gjson.GetBytes(raw, "error.details").Array() {
		if antigravity {
			if ts := d.Get("metadata.quotaResetTimeStamp").String(); ts != "" {
				if t, err := time.Parse(time.RFC3339, ts); err == nil && t.After(now) {
					return t, true
				}
			}
		}
		delay := d.Get("retryDelay").String()
		if !antigravity && d.Get("@type").String() != "type.googleapis.com/google.rpc.RetryInfo" {
			continue
		}
		if sec, err := strconv.ParseFloat(strings.TrimSuffix(delay, "s"), 64); err == nil && sec > 0 && sec < 86400*7 {
			return now.Add(time.Duration(sec * float64(time.Second))), true
		}
	}
	return time.Time{}, false
}

// antigravityDelay says how long to wait before trying again, or false when the
// failure should go to the next account: a reset hint over the ceiling, or a
// failure with no hint that is not a known transient one.
func antigravityDelay(h http.Header, raw []byte, status, tries int, now time.Time) (time.Duration, bool) {
	hint := headerDelay(h, now)
	if hint == 0 {
		hint = textDelay(raw)
	}
	if hint > 0 {
		return hint, hint <= retryCeiling
	}
	if status != http.StatusTooManyRequests && status < 500 && !transientWords.Match(raw) {
		return 0, false
	}
	ceiling := transientCeiling
	if status == http.StatusTooManyRequests {
		ceiling = retryCeiling
	}
	return min(time.Second<<tries, ceiling), true
}

// headerDelay reads Retry-After (seconds or a date) and the rate-limit headers.
func headerDelay(h http.Header, now time.Time) time.Duration {
	if v := h.Get("Retry-After"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
		if t, err := http.ParseTime(v); err == nil && t.After(now) {
			return t.Sub(now)
		}
	}
	if n, err := strconv.Atoi(h.Get("X-RateLimit-Reset-After")); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	if n, err := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64); err == nil {
		if d := time.Unix(n, 0).Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

// textDelay reads "reset after 1h30m" in an error's text.
func textDelay(raw []byte) time.Duration {
	m := resetAfter.FindStringSubmatch(string(raw))
	if m == nil {
		return 0
	}
	var d time.Duration
	for i, unit := range []time.Duration{time.Hour, time.Minute, time.Second} {
		if m[i+1] == "" {
			continue
		}
		n, _ := strconv.Atoi(strings.TrimRight(m[i+1], "hms"))
		d += time.Duration(n) * unit
	}
	return d
}

// geminiCLIAgent fills the model into the user agent the data names, which holds
// the tool, its version and the fixed system words, with {model} where the model
// goes.
func geminiCLIAgent(template, model string) string {
	if template == "" {
		return "GeminiCLI/unknown (router; router; terminal)"
	}
	if model == "" {
		model = "unknown"
	}
	return strings.ReplaceAll(template, "{model}", model)
}
