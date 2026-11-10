package executor

import (
	"context"
	"crypto/tls"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/upstream"
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
	Default *Default
	Lane    openCodeLane
	// Client is the client this lane goes through: its TLS offer is part of the
	// free gate. Nil takes the shared one.
	Client *http.Client
}

// Execute sends to the lane the model is served on.
func (o *OpenCodeFree) Execute(ctx context.Context, req Request) (Response, error) {
	lane := o.lane()
	req.URL = openCodeURL(lane, req.Model)
	req.Body = openCodeFreeBody(req.Body, openCodeIsResponses(req.Model), openCodeIsMessages(req.Model))
	if renames := openCodeRenames(req.Body); len(renames) > 0 && req.Renamed != nil {
		req.Renamed(renames)
	}
	req.Body = openCodeWithoutRenames(req.Body)
	session := opencodeSessionId(openCodeSeed(req))
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
	return d.Execute(ctx, req)
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
func openCodeSeed(req Request) string {
	if req.Session != "" {
		return "opencode\x00" + req.Session
	}
	return "opencode\x00" + req.Model
}

// openCodeURL picks the lane's address for the model.
func openCodeURL(l openCodeLane, model string) string {
	base := openCodeBaseModel(model)
	switch {
	case openCodeIsResponses(base) && l.responses != "":
		return l.responses
	case openCodeIsMessages(base) && l.messages != "":
		return l.messages
	}
	return l.chat
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
func openCodeFreeBody(body []byte, responses, messages bool) []byte {
	body = setValue(body, "stream", true)
	if responses {
		body = openCodeResponsesBody(body, nil)
		body = openCodeFingerprint(body, true)
		return body
	}
	body = openCodeDropAutoEffort(body)
	body = openCodeFingerprint(body, false)
	if messages {
		return body
	}
	return body
}

// openCodeDropAutoEffort leaves "auto" out, which the plain Chat lane refuses
// to read.
func openCodeDropAutoEffort(body []byte) []byte {
	if gjson.GetBytes(body, "reasoning_effort").String() == "auto" {
		return remove(body, "reasoning_effort")
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
