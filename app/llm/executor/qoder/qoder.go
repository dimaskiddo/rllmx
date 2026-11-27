package qoder

import (
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"rllmx/app/llm/executor"
	"rllmx/app/llm/upstream"
	"rllmx/app/platform/cache"
	"rllmx/app/platform/text"
	"rllmx/app/protocol/cosy"
	"rllmx/app/store/dialect"
	"rllmx/app/store/document"
)

// The two regional sites. The international one serves a job token from a
// different host than a device token; the other serves everything from one.
const (
	qoderIntlChat    = "https://api3.qoder.sh"
	qoderIntlChatAlt = "https://api2.qoder.sh"
	qoderCNChat      = "https://gateway.qoder.com.cn"

	qoderChatPath  = "/api/v2/service/pro/sse/agent_chat_generation"
	qoderMaxTokens = 32768
)

// Qoder sends to the gateway's own protocol: the body is encoded, the headers
// are signed, and the answer is an envelope around ordinary chat chunks.
// Public is the vendor's key, read from the provider's data.
type Qoder struct {
	Default *executor.Default
	// CN marks the second site, which has its own host and one gateway.
	CN     bool
	Public *rsa.PublicKey
	// Cache keeps each account's model list; nil fetches it every call.
	Cache *cache.Cache
	// Sleep waits between queue attempts; nil waits on a real timer.
	Sleep func(ctx context.Context, d time.Duration) error
	// PeekIdle ends the look at the start of an answer when no byte arrives for
	// this long.
	PeekIdle time.Duration
	Now      func() time.Time
}

func (q *Qoder) now() time.Time {
	if q.Now != nil {
		return q.Now()
	}
	return time.Now()
}

// regionOf is the site a request goes to.
func (q *Qoder) regionOf(executor.Request) string {
	if q.CN {
		return "cn"
	}
	return "intl"
}

// host is the gateway's own host. A job token is served by the international
// site's second host and refused by the first.
func (q *Qoder) host(req executor.Request) string {
	if q.CN {
		return qoderCNChat
	}
	if strings.HasPrefix(req.Key, "jt-") {
		return qoderIntlChatAlt
	}
	return qoderIntlChat
}

// identity is who the request is signed for.
func (q *Qoder) identity(req executor.Request) cosy.Identity {
	return cosy.Identity{
		UserID: executor.Field(req, "userId"), Token: req.Key,
		Name: executor.Field(req, "displayName"), Email: executor.Field(req, "email"),
		MachineID: qoderMachineID(executor.Field(req, "machineId"), executor.Field(req, "userId")),
		NewID:     dialect.NewID, IDEVersion: "1.0.0", ClientType: "5", MachineOS: "x86_64_windows", MachineType: "5",
	}
}

// qoderMachineID is the account's stored machine, else one derived from its user
// id, so a request from the same account always looks like the same device. A
// new device rejoins the back of the model queue.
func qoderMachineID(stored, userID string) string {
	if stored != "" {
		return stored
	}
	sum := sha256.Sum256([]byte("qoder-machine\x00" + userID))
	h := hex.EncodeToString(sum[:])
	return h[:8] + "-" + h[8:12] + "-5" + h[13:16] + "-a" + h[17:20] + "-" + h[20:32]
}

// Execute sends one call: the body is built from the account's model list,
// encoded and signed, and the answer is held from its first frame.
func (q *Qoder) Execute(ctx context.Context, req executor.Request) (executor.Response, error) {
	if req.Key == "" {
		return executor.Response{}, executor.ErrNoCredential
	}
	if q.Public == nil {
		return executor.Response{}, executor.ErrBadAccount
	}
	cat, err := q.catalog(ctx, req)
	if err != nil {
		return executor.Response{}, err
	}
	key := qoderKey(req.Model)
	block, ok := cat.block(key)
	if !ok {
		return executor.Response{}, ErrNoModelConfig
	}
	body, err := qoderBody(req, key, block)
	if err != nil {
		return executor.Response{}, executor.ErrBadAccount
	}
	encoded := cosy.Encode(body)
	target := q.host(req) + "/algo" + qoderChatPath + "?FetchKeys=llm_model_result&AgentId=agent_common&Encode=1"
	info, err := q.signedHeaders(target, []byte(encoded), req)
	if err != nil {
		return executor.Response{}, executor.ErrBadAccount
	}
	info["X-Model-Key"] = key
	info["X-Model-Source"] = gjson.Get(block, "source").String()
	if info["X-Model-Source"] == "" {
		info["X-Model-Source"] = "system"
	}
	req.URL = target
	req.Body = []byte(encoded)
	req.Stream = true
	req.NoRedirect = true
	// The signature covers the encoded body, so it must leave as signed.
	req.NoCompress = true
	// The credential is the signature, so the auth block is replaced rather than
	// inherited: it would put the token in a plain header.
	req.Auth = &upstream.Auth{Headers: info}
	return q.send(ctx, req)
}

// signedHeaders builds one request's signature. Every resend needs a fresh one:
// the signature carries the request id, and a reused one is refused.
func (q *Qoder) signedHeaders(target string, body []byte, req executor.Request) (map[string]string, error) {
	h, err := cosy.Headers(q.Public, target, body, q.identity(req))
	if err != nil {
		return nil, err
	}
	h["Content-Type"] = "application/json"
	h["Accept"] = "text/event-stream"
	h["Cache-Control"] = "no-cache"
	// Compression changes the body the gateway validates against the signature.
	h["Accept-Encoding"] = "identity"
	return h, nil
}

// send reads the answer from its first frame. A model that is queued is asked
// for again until it is ready or the wait is spent; a refusal that stands
// before any output becomes the status it names.
func (q *Qoder) send(ctx context.Context, req executor.Request) (executor.Response, error) {
	deadline := q.now().Add(qoderQueueMaxWait)
	for {
		resp, err := q.Default.Send(ctx, req)
		if err != nil {
			return executor.Response{}, err
		}
		if resp.Status < 200 || resp.Status > 299 {
			return resp, nil
		}
		status, body, seen := qoderPeek(ctx, resp.Body, q.PeekIdle)
		if status != 0 {
			return executor.Response{Status: status, Header: resp.Header, Body: http.NoBody}, nil
		}
		if queue := qoderQueueOf(seen); queue != nil {
			_ = body.Close()
			wait := qoderQueueDelay(queue, false)
			if q.now().Add(wait).After(deadline) {
				return executor.Response{Status: http.StatusServiceUnavailable, Header: resp.Header, Body: http.NoBody}, nil
			}
			if werr := q.wait(ctx, wait); werr != nil {
				return executor.Response{}, werr
			}
			continue
		}
		resp.Body = body
		resp.Header = executor.StreamHeader(resp.Header)
		return resp, nil
	}
}

func (q *Qoder) wait(ctx context.Context, d time.Duration) error {
	if q.Sleep != nil {
		return q.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// qoderPeek reads the envelope's first frame. A refused status becomes the status
// it names, and the answer is closed; anything else is replayed whole.
func qoderPeek(ctx context.Context, body io.ReadCloser, idle time.Duration) (int, io.ReadCloser, []byte) {
	return executor.PeekUntil(ctx, body, idle, func(seen []byte) (int, bool, bool) {
		if st, refused := qoderFrameStatus(seen); refused {
			return st, true, false
		}
		return 0, false, qoderHasFrame(seen)
	})
}

// qoderFrameStatus reads the first envelope and reports a refusal with the
// status it carries, or that none has arrived yet.
func qoderFrameStatus(seen []byte) (int, bool) {
	line, ok := firstQoderData(seen)
	if !ok {
		return 0, false
	}
	if line == "[DONE]" {
		return 0, false
	}
	e := gjson.Parse(line)
	if !e.IsObject() {
		return 0, false
	}
	status := int(e.Get("statusCodeValue").Int())
	if status == 0 {
		status = http.StatusOK
	}
	if status == http.StatusOK {
		return 0, false
	}
	// A quota or billing refusal is answered as refused access, which is what the
	// account rules read as "this account cannot serve it".
	inner := e.Get("body").String()
	if qoderBilling(inner) {
		return http.StatusForbidden, true
	}
	if status < 400 || status > 599 {
		return http.StatusBadGateway, true
	}
	return status, true
}

// qoderQueueOf reads a queue signal from what has arrived, looking inside the
// envelope: the signal travels in the envelope's own body, often as a JSON
// string inside another.
func qoderQueueOf(seen []byte) *qoderQueue {
	line, ok := firstQoderData(seen)
	if !ok || line == "[DONE]" {
		return nil
	}
	e := gjson.Parse(line)
	if e.IsObject() && e.Get("body").Exists() {
		if info := qoderQueueInfo([]byte(e.Get("body").String())); info != nil {
			return info
		}
	}
	return qoderQueueInfo([]byte(line))
}

// qoderHasFrame reports whether one envelope line has arrived whole.
func qoderHasFrame(seen []byte) bool {
	_, ok := firstQoderData(seen)
	return ok
}

// firstQoderData returns the first whole `data:` line of an answer.
func firstQoderData(seen []byte) (string, bool) {
	for _, raw := range strings.Split(string(seen), "\n") {
		if payload, framed := text.DataLine(raw); framed {
			return payload, true
		}
	}
	return "", false
}

// qoderBilling reports a refusal about the account's quota rather than the
// request, which the account rules answer differently.
func qoderBilling(inner string) bool {
	if inner == "" {
		return false
	}
	if strings.Contains(strings.ToLower(inner), "pricingurl") {
		return true
	}
	switch gjson.Get(inner, "code").String() {
	case "110", "112":
		return true
	}
	return strings.Contains(inner, `"code":"112"`)
}

// qoderBody maps a chat request to the gateway's shape: the system text lifted
// out, content flattened, the model's own configuration attached, and the two
// record ids derived from the conversation so a replay keeps them.
func qoderBody(req executor.Request, key, block string) ([]byte, error) {
	src := gjson.ParseBytes(req.Body)
	if !src.IsObject() {
		return nil, executor.ErrBadAccount
	}
	system, messages := qoderMessages(src.Get("messages"))
	lastUser := qoderLastUser(messages)
	maxTokens := qoderMaxTokens
	if m := gjson.Get(block, "max_output_tokens").Int(); m > 0 {
		maxTokens = int(m)
	}
	for _, p := range []string{"max_tokens", "max_completion_tokens"} {
		if v := src.Get(p).Int(); v > 0 && int(v) < maxTokens {
			maxTokens = int(v)
		}
	}
	record := qoderRecordID(key, messages, src.Get("tools").Raw, maxTokens)
	session := qoderSessionID(req, key)
	requestID := dialect.NewID()
	if requestID == "" {
		return nil, executor.ErrBadAccount
	}
	payload := `{}`
	payload, _ = sjson.Set(payload, "request_id", requestID)
	payload, _ = sjson.Set(payload, "request_set_id", record)
	payload, _ = sjson.Set(payload, "chat_record_id", record)
	payload, _ = sjson.Set(payload, "session_id", session)
	payload, _ = sjson.Set(payload, "stream", true)
	payload, _ = sjson.Set(payload, "chat_task", "FREE_INPUT")
	payload, _ = sjson.Set(payload, "is_reply", true)
	payload, _ = sjson.Set(payload, "is_retry", false)
	payload, _ = sjson.Set(payload, "source", 1)
	payload, _ = sjson.Set(payload, "version", "3")
	payload, _ = sjson.Set(payload, "session_type", "qodercli")
	payload, _ = sjson.Set(payload, "agent_id", "agent_common")
	payload, _ = sjson.Set(payload, "task_id", "common")
	payload, _ = sjson.Set(payload, "code_language", "")
	payload, _ = sjson.Set(payload, "chat_prompt", "")
	payload, _ = sjson.SetRaw(payload, "image_urls", "null")
	payload, _ = sjson.Set(payload, "aliyun_user_type", "")
	payload, _ = sjson.Set(payload, "system", system)
	if messages != "" {
		payload, _ = sjson.SetRaw(payload, "messages", messages)
	} else {
		payload, _ = sjson.SetRaw(payload, "messages", "[]")
	}
	if tools := src.Get("tools"); tools.IsArray() {
		payload, _ = sjson.SetRaw(payload, "tools", tools.Raw)
	} else {
		payload, _ = sjson.SetRaw(payload, "tools", "[]")
	}
	payload, _ = sjson.Set(payload, "parameters.max_tokens", maxTokens)
	payload, _ = sjson.Set(payload, "chat_context.chatPrompt", "")
	payload, _ = sjson.SetRaw(payload, "chat_context.imageUrls", "null")
	payload, _ = sjson.SetRaw(payload, "chat_context.extra.context", "[]")
	payload, _ = sjson.Set(payload, "chat_context.extra.modelConfig.key", key)
	payload, _ = sjson.Set(payload, "chat_context.extra.modelConfig.is_reasoning", gjson.Get(block, "is_reasoning").Bool())
	payload, _ = sjson.Set(payload, "chat_context.extra.originalContent", lastUser)
	payload, _ = sjson.SetRaw(payload, "chat_context.features", "[]")
	payload, _ = sjson.Set(payload, "chat_context.text", lastUser)
	payload, _ = sjson.SetRaw(payload, "model_config", block)
	payload, _ = sjson.Set(payload, "business.product", "cli")
	payload, _ = sjson.Set(payload, "business.version", "1.0.0")
	payload, _ = sjson.Set(payload, "business.type", "agent")
	payload, _ = sjson.Set(payload, "business.stage", "start")
	payload, _ = sjson.Set(payload, "business.id", requestID)
	payload, _ = sjson.Set(payload, "business.name", qoderCut(lastUser, 30))
	payload, _ = sjson.Set(payload, "business.begin_at", time.Now().UnixMilli())
	return []byte(payload), nil
}

// qoderMessages lifts the system turns out and flattens each turn's content.
// The gateway refuses a system message inside the list.
func qoderMessages(msgs gjson.Result) (system, mapped string) {
	var systemParts []string
	parts := make([]string, 0, len(msgs.Array()))
	for _, m := range msgs.Array() {
		if !m.IsObject() {
			continue
		}
		role := m.Get("role").String()
		if role == "system" || role == "developer" {
			if t := qoderText(m.Get("content")); t != "" {
				systemParts = append(systemParts, t)
			}
			continue
		}
		out := `{}`
		out, _ = sjson.Set(out, "role", role)
		out, _ = sjson.SetRaw(out, "content", qoderContent(m.Get("content")))
		if id := m.Get("tool_call_id").String(); id != "" {
			out, _ = sjson.Set(out, "tool_call_id", id)
		}
		if name := m.Get("name").String(); name != "" {
			out, _ = sjson.Set(out, "name", name)
		}
		if calls := m.Get("tool_calls"); calls.IsArray() {
			out, _ = sjson.SetRaw(out, "tool_calls", calls.Raw)
		}
		parts = append(parts, out)
	}
	return strings.Join(systemParts, "\n\n"), "[" + strings.Join(parts, ",") + "]"
}

// qoderContent flattens one turn's content. Text becomes a plain string; an
// image keeps the content an array, because a link is the only form the gateway
// takes without an upload. A file becomes a short stub: its bytes have no place
// in a chat body.
func qoderContent(content gjson.Result) string {
	switch {
	case content.Type == gjson.String:
		return document.Encode(content.String())
	case !content.IsArray():
		return document.Encode("")
	}
	var texts []string
	var blocks []string
	hasImage := false
	push := func(t string) {
		if t == "" {
			return
		}
		if hasImage || len(blocks) > 0 {
			blocks = append(blocks, `{"type":"text","text":`+document.Encode(t)+`}`)
			return
		}
		texts = append(texts, t)
	}
	for _, item := range content.Array() {
		if !item.IsObject() {
			continue
		}
		switch item.Get("type").String() {
		case "image_url", "image":
			if link := qoderImageURL(item); link != "" {
				blocks = append(blocks, `{"type":"image_url","image_url":{"url":`+document.Encode(link)+`}}`)
				hasImage = true
			}
		case "file", "document":
			name := document.FirstString(item.Get("file.filename"), item.Get("file.name"), item.Get("title"))
			if name == "" {
				name = "file"
			}
			push("[file omitted: " + qoderCut(name, 120) + " — the gateway reads documents through its own file API, not inlined bytes]")
		default:
			push(item.Get("text").String())
		}
	}
	if !hasImage {
		return document.Encode(strings.Join(texts, "\n"))
	}
	if len(texts) > 0 {
		blocks = append([]string{`{"type":"text","text":` + document.Encode(strings.Join(texts, "\n")) + `}`}, blocks...)
	}
	return "[" + strings.Join(blocks, ",") + "]"
}

// qoderImageURL is the link an image part carries, from either shape.
func qoderImageURL(item gjson.Result) string {
	if link := item.Get("image_url.url").String(); link != "" {
		return link
	}
	if link := item.Get("image_url").String(); link != "" {
		return link
	}
	if src := item.Get("source"); src.IsObject() {
		if src.Get("type").String() == "base64" && src.Get("data").String() != "" {
			media := src.Get("media_type").String()
			if media == "" {
				media = "image/png"
			}
			return "data:" + media + ";base64," + src.Get("data").String()
		}
		return src.Get("url").String()
	}
	return ""
}

// qoderText reads every text part of a content value.
func qoderText(content gjson.Result) string {
	if content.Type == gjson.String {
		return content.String()
	}
	var parts []string
	for _, item := range content.Array() {
		if t := item.Get("text").String(); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n")
}

// qoderLastUser is the text of the last user turn, which the gateway repeats in
// two fields of its own.
func qoderLastUser(mapped string) string {
	var last string
	for _, m := range gjson.Parse(mapped).Array() {
		if m.Get("role").String() != "user" {
			continue
		}
		if c := m.Get("content"); c.Type == gjson.String {
			last = c.String()
		} else {
			last = qoderText(c)
		}
	}
	return last
}

// qoderSessionID names the conversation, bound to the account so two accounts do
// not share one session.
func qoderSessionID(req executor.Request, key string) string {
	seed := req.Session
	if seed == "" {
		seed = executor.Field(req, "userId")
	}
	return qoderHash("qoder-session", seed, key)
}

// qoderRecordID names the prompt, so the same conversation keeps one record.
func qoderRecordID(key, messages, tools string, maxTokens int) string {
	return qoderHash("qoder-record", key, messages, tools, "mt="+strconv.Itoa(maxTokens))
}

// qoderHash is sixteen hex of a digest over the parts.
func qoderHash(prefix string, parts ...string) string {
	h := sha256.New()
	h.Write([]byte(prefix))
	for _, p := range parts {
		h.Write([]byte{0})
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// qoderCut shortens text to n characters, marking the cut.
func qoderCut(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}
