package opencode

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/executor"
	"rllmx/app/llm/upstream"
)

// The free lane's fixed identity. It is what the gateway's free tier expects of
// a client, not this program's own name.
const (
	openCodeFreeAgent   = "opencode/latest/2.0.26/cli"
	openCodePublicKey   = "Bearer public"
	openCodeClient      = "cli"
	openCodeProject     = "global"
	openCodeSessionHead = "x-opencode-session"
	openCodeMessagesVer = "2023-06-01"
)

// OpenCode's free lane. Nothing authenticates it beyond the client's shape, so
// its headers, its session id and its body are all part of the gate.
type openCodeLane struct {
	// responses, messages and chat name the three addresses of the lane.
	responses, messages, chat string
	// ua is the user agent a client of the current version sends.
	ua string
	// client and project are the tool's own words.
	client, project string
}

// OpenCodeFree serves the gateway's free lane.
type OpenCodeFree struct {
	Default *executor.Default
	Lane    openCodeLane
	// Client is the client this lane goes through: its TLS offer is part of the
	// free gate. Nil takes the shared one.
	Client *http.Client
	// Now is the clock for a session generation's life; nil is time.Now.
	Now func() time.Time
	// Sleep is the overload wait's timer source; nil sleeps for real.
	Sleep func(time.Duration) <-chan time.Time
	// gens keeps a conversation's session generation after a rejection.
	gens sessionGens
}

const (
	// openCodeOverloadTries bounds the free lane's own retry of a 429 that means
	// an overloaded backend rather than a spent account.
	openCodeOverloadTries  = 8
	openCodeOverloadBase   = 800 * time.Millisecond
	openCodeOverloadCap    = 4 * time.Second
	openCodeOverloadJitter = 250 * time.Millisecond
	openCodeBodyPeek       = 4 << 10
)

// Execute sends to the lane the model is served on.
func (o *OpenCodeFree) Execute(ctx context.Context, req executor.Request) (executor.Response, error) {
	lane := o.lane()
	req.URL = openCodeURL(lane, openCodeEntryBase(req), req.Model)
	req.Body = openCodeFreeBody(req.Body, openCodeIsResponses(req.Model))
	// The free lane's 1.3 model is only accepted with a tool choice present.
	if strings.Contains(req.Model, "muse-spark-1.3") {
		req.Body = executor.SetValue(req.Body, "tool_choice", "auto")
	}
	if renames := openCodeRenames(req.Body); len(renames) > 0 && req.Renamed != nil {
		req.Renamed(renames)
	}
	req.Body = openCodeWithoutRenames(req.Body)
	seed := openCodeSeed(req)
	session := opencodeSessionID(seedWithGen(seed, o.gens.get(seed, o.now())))
	d := o.Default
	if o.Client != nil {
		copied := *o.Default
		copied.Client = o.Client
		d = &copied
	}
	// The lane authenticates nobody: its identity is the client's shape, and the
	// fixed word is what an unauthenticated client sends.
	req.Auth = &upstream.Auth{Headers: map[string]string{
		"Authorization":         openCodePublicKey,
		"User-Agent":            lane.ua,
		"x-opencode-client":     lane.client,
		openCodeSessionHead:     session,
		"x-opencode-session-id": session,
		"x-session-affinity":    session,
		"x-session-id":          session,
		"x-opencode-project":    lane.project,
		"Accept":                "*/*",
	}}
	if strings.HasSuffix(req.URL, "/messages") {
		req.Auth.Headers["anthropic-version"] = openCodeMessagesVer
	}
	return o.send(ctx, d, req, seed)
}

// send retries a 429 that names an overloaded backend with a rising wait, and
// retires the conversation's session id on any other refusal, so the next turn
// asks for a fresh one.
func (o *OpenCodeFree) send(ctx context.Context, d executor.Executor, req executor.Request, seed string) (executor.Response, error) {
	for try := 1; ; try++ {
		resp, err := d.Execute(ctx, req)
		if err != nil {
			return resp, err
		}
		switch resp.Status {
		case http.StatusTooManyRequests:
			body, overloaded := peekBody(resp.Body, openCodeBodyPeek)
			resp.Body = body
			if !overloaded {
				o.gens.bump(seed, o.now())
				return resp, nil
			}
			if try >= openCodeOverloadTries {
				return resp, nil
			}
			executor.Drain(resp.Body)
			if err := executor.Wait(ctx, openCodeOverloadWait(try), o.Sleep); err != nil {
				return executor.Response{}, err
			}
		case http.StatusForbidden:
			o.gens.bump(seed, o.now())
			return resp, nil
		default:
			return resp, nil
		}
	}
}

func (o *OpenCodeFree) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// openCodeOverloadWait is the wait after the given attempt: a linear ramp capped
// at four seconds, plus up to 250 ms so two clients do not return together.
func openCodeOverloadWait(try int) time.Duration {
	d := time.Duration(try) * openCodeOverloadBase
	if d > openCodeOverloadCap {
		d = openCodeOverloadCap
	}
	var b [2]byte
	if _, err := rand.Read(b[:]); err == nil {
		d += time.Duration(binary.BigEndian.Uint16(b[:])%uint16(openCodeOverloadJitter/time.Millisecond)) * time.Millisecond
	}
	return d
}

// peekBody reads at most n bytes to read the body's start, then hands the rest
// out unchanged. It reports whether the text names an overloaded backend.
func peekBody(rc io.ReadCloser, n int) (io.ReadCloser, bool) {
	if rc == nil {
		return nil, false
	}
	head := make([]byte, n)
	read, _ := io.ReadFull(rc, head)
	head = head[:read]
	overloaded := bytes.Contains(bytes.ToLower(head), []byte("overloaded"))
	return readCloser{Reader: io.MultiReader(bytes.NewReader(head), rc), Closer: rc}, overloaded
}

type readCloser struct {
	io.Reader
	io.Closer
}

func (o *OpenCodeFree) lane() openCodeLane {
	l := o.Lane
	if l.ua == "" {
		l.ua = openCodeFreeAgent
	}
	if l.client == "" {
		l.client = openCodeClient
	}
	if l.project == "" {
		l.project = openCodeProject
	}
	return l
}

// openCodeSeed is the conversation the session id belongs to. It is the keyed
// session the pipeline derived, so one conversation keeps one id.
func openCodeSeed(req executor.Request) string {
	if req.Session != "" {
		return "opencode\x00" + req.Session
	}
	return "opencode\x00" + req.Model
}

// openCodeEntryBase is the address the provider's own data names, which the
// lane's paths are built on.
func openCodeEntryBase(req executor.Request) string {
	if req.Entry.Transport.BaseURL != "" {
		return req.Entry.Transport.BaseURL
	}
	return req.URL
}

// openCodeURL picks the lane's address for the model: one the lane names wins,
// else the provider's own base gains the lane's path. The data names the chat
// address, so the base is cut back to the host first.
func openCodeURL(l openCodeLane, entryBase, model string) string {
	base := openCodeBaseModel(model)
	if openCodeIsResponses(base) {
		if l.responses != "" {
			return l.responses
		}
		return openCodeRoot(entryBase) + "/inference/v1/responses"
	}
	if openCodeIsMessages(base) {
		if l.messages != "" {
			return l.messages
		}
		return openCodeRoot(entryBase) + "/inference/v1/messages"
	}
	if l.chat != "" {
		return l.chat
	}
	return openCodeRoot(entryBase) + "/inference/v1/chat/completions"
}

// openCodeRoot cuts a known endpoint off an address, leaving the host the lane's
// own paths hang from.
func openCodeRoot(base string) string {
	base = strings.TrimSpace(base)
	for _, suffix := range []string{
		"/inference/v1/chat/completions", "/inference/v1/responses", "/inference/v1/messages",
		"/v1/chat/completions", "/chat/completions",
	} {
		if strings.HasSuffix(base, suffix) {
			return strings.TrimSuffix(base, suffix)
		}
	}
	return strings.TrimSuffix(base, "/")
}

// openCodeBaseModel drops a thinking suffix such as "model(high)", so a lookup
// sees the id the registry knows.
func openCodeBaseModel(model string) string {
	if i := strings.LastIndexByte(model, '('); i > 0 && strings.HasSuffix(model, ")") {
		return strings.TrimSpace(model[:i])
	}
	return strings.TrimSpace(model)
}

// openCodeIsResponses and openCodeIsMessages name the models each lane serves.
func openCodeIsResponses(model string) bool {
	return strings.Contains(model, "muse-spark-1.2") || strings.Contains(model, "muse-spark-1.3")
}

func openCodeIsMessages(model string) bool { return openCodeBaseModel(model) == "union-alpha" }

// openCodeFreeBody fits the body to the lane: the forced stream, the Responses
// rewrite when the lane is one, the free tier's fingerprint tools and the
// environment marker.
func openCodeFreeBody(body []byte, responses bool) []byte {
	body = executor.SetValue(body, "stream", true)
	if responses {
		body = openCodeResponsesBody(body, nil)
		body = openCodeFingerprint(body, true)
		return body
	}
	body = openCodeDropAutoEffort(body)
	return openCodeFingerprint(body, false)
}

// openCodeDropAutoEffort leaves "auto" out, which the plain Chat lane refuses
// to read.
func openCodeDropAutoEffort(body []byte) []byte {
	if gjson.GetBytes(body, "reasoning_effort").String() == "auto" {
		return executor.Remove(body, "reasoning_effort")
	}
	return body
}

// OpenCodeCipherSuites are the TLS 1.2 suites the free lane offers. The gate
// reads the offer, so the list is the tool's own rather than the standard one.
var OpenCodeCipherSuites = []uint16{
	tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
	tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
	tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
	tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
	tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
	tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
	tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA,
	tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA,
	tls.TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA,
	tls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA,
	tls.TLS_RSA_WITH_AES_128_GCM_SHA256,
	tls.TLS_RSA_WITH_AES_256_GCM_SHA384,
	tls.TLS_RSA_WITH_AES_128_CBC_SHA,
	tls.TLS_RSA_WITH_AES_256_CBC_SHA,
}
