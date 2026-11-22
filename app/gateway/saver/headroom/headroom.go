package headroom

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"rllmx/app/llm/translate"
)

// Headroom client bounds. The body a sidecar may answer is capped, and a request
// never waits longer than the largest allowed timeout.
const (
	// DefaultURL is the sidecar's address when neither the settings row nor the
	// configuration names one.
	DefaultURL = "http://localhost:8787"
	// DefaultTimeoutMS, MinTimeoutMS and MaxTimeoutMS bound the per-call wait the
	// settings row may name.
	DefaultTimeoutMS = 3000
	MinTimeoutMS     = 100
	MaxTimeoutMS     = 60000

	headroomDefaultTimeout = DefaultTimeoutMS * time.Millisecond
	headroomMinTimeout     = MinTimeoutMS * time.Millisecond
	headroomMaxTimeout     = MaxTimeoutMS * time.Millisecond
	headroomAnswerCap      = 8 << 20
	headroomPath           = "/v1/compress"
)

// Options is the sidecar's configuration for one request.
type Options struct {
	Enabled              bool
	URL                  string
	CompressUserMessages bool
	Timeout              time.Duration
}

// Result reports what one call did, for a log line: counts and a short reason
// only, never prompt text and never the address.
type Result struct {
	Compressed bool
	Note       string
}

// Compressor posts eligible text to the Headroom sidecar, which answers
// compressed replacements. It is a client only: the sidecar is the operator's to
// install and run.
type Compressor struct {
	// Client builds the HTTP client for one sidecar host. The pipeline's builder
	// permits exactly that host in the outbound guard, so the address comes from
	// the operator's setting and never from a request.
	Client func(host string) *http.Client

	mu      sync.Mutex
	clients map[string]*http.Client
}

// client is the guarded client for a host, built once per host seen.
func (c *Compressor) client(host string) *http.Client {
	if c.Client == nil {
		return http.DefaultClient
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.clients == nil {
		c.clients = map[string]*http.Client{}
	}
	if got, ok := c.clients[host]; ok {
		return got
	}
	made := c.Client(host)
	c.clients[host] = made
	return made
}

// headroomMessage is one row sent to and answered by the sidecar.
type headroomMessage struct {
	Role       string `json:"role"`
	Content    string `json:"content"`
	ToolCallID string `json:"tool_call_id,omitempty"`
}

// Apply compresses tool results, and user messages when the setting allows it.
// A row is replaced only when the answer keeps its role and id, is non-empty and
// is shorter; one mismatch discards the whole answer.
func (c *Compressor) Apply(ctx context.Context, r *translate.Request, o Options) Result {
	endpoint, err := compressEndpoint(o.URL)
	if err != nil {
		return Result{Note: "off: bad address"}
	}
	rows, targets := headroomRows(r, o.CompressUserMessages)
	if len(rows) == 0 {
		return Result{Note: "skipped: no eligible text"}
	}
	answer, err := c.post(ctx, endpoint, r.Model, o, rows)
	if err != nil {
		return Result{Note: "failed"}
	}
	if len(answer) != len(rows) {
		return Result{Note: "failed: row count changed"}
	}
	for i := range rows {
		if answer[i].Role != rows[i].Role || answer[i].ToolCallID != rows[i].ToolCallID {
			return Result{Note: "failed: rows reordered"}
		}
	}
	var res Result
	for i, row := range answer {
		target := targets[i]
		if target == nil || row.Content == "" || len(row.Content) >= len(target.Text) {
			continue
		}
		target.Text = row.Content
		res.Compressed = true
	}
	if !res.Compressed {
		res.Note = "skipped: no smaller answer"
	}
	return res
}

// post is the one outbound call. The answer is read under a size cap, so a
// sidecar cannot fill memory.
func (c *Compressor) post(ctx context.Context, endpoint, model string, o Options, rows []headroomMessage) ([]headroomMessage, error) {
	payload := struct {
		Messages []headroomMessage `json:"messages"`
		Model    string            `json:"model"`
		Config   map[string]bool   `json:"config,omitempty"`
	}{Messages: rows, Model: model}
	if o.CompressUserMessages {
		payload.Config = map[string]bool{"compress_user_messages": true}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = headroomDefaultTimeout
	}
	timeout = min(max(timeout, headroomMinTimeout), headroomMaxTimeout)
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(cctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.client(req.URL.Hostname()).Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4<<10))
		return nil, errors.New("saver: the sidecar refused")
	}
	var answer struct {
		Messages []headroomMessage `json:"messages"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, headroomAnswerCap)).Decode(&answer); err != nil {
		return nil, err
	}
	if len(answer.Messages) == 0 {
		return nil, errors.New("saver: the sidecar answered no rows")
	}
	return answer.Messages, nil
}

// headroomRows projects the eligible messages: every successful tool result, and
// user text when the setting allows it. A message with more than one part is
// left alone, because the answer could only fill one of them.
func headroomRows(r *translate.Request, userMessages bool) ([]headroomMessage, []*translate.Part) {
	var rows []headroomMessage
	var targets []*translate.Part
	for i := range r.Messages {
		m := &r.Messages[i]
		switch {
		case m.Role == translate.RoleTool && !m.Error:
		case m.Role == translate.RoleUser && userMessages:
		default:
			continue
		}
		if len(m.Content) != 1 || m.Content[0].Type != translate.PartText || m.Content[0].Text == "" {
			continue
		}
		rows = append(rows, headroomMessage{Role: string(m.Role), Content: m.Content[0].Text, ToolCallID: m.ToolCallID})
		targets = append(targets, &m.Content[0])
	}
	return rows, targets
}

// compressEndpoint joins the sidecar's base address with the compress path,
// refusing anything but a plain http(s) address with a host.
func compressEndpoint(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" {
		return "", errors.New("saver: bad sidecar address")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("saver: bad sidecar scheme")
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + headroomPath
	return u.String(), nil
}
