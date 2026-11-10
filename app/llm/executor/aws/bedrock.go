package aws

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/tidwall/sjson"

	"rllmx/app/llm/executor"
	"rllmx/app/llm/upstream"
	"rllmx/app/protocol/sigv4"
)

// Bedrock's fixed host and path pieces.
const (
	bedrockHost         = "https://bedrock-runtime."
	bedrockSuffix       = ".amazonaws.com/model/"
	bedrockInvoke       = "/invoke"
	bedrockInvokeStream = "/invoke-with-response-stream"
	bedrockService      = "bedrock"
	// bedrockAnthropicVersion is the version pin Bedrock's Anthropic endpoint
	// requires in place of the model, which lives in the address.
	bedrockAnthropicVersion = "bedrock-2023-05-31"
	bedrockDefaultRegion    = "us-east-1"
	// bedrockStreamAccept is the content type Bedrock frames a streamed answer in.
	bedrockStreamAccept = "application/vnd.amazon.eventstream"
)

// modelFamily picks the models one Bedrock entry serves. A family's request and
// response shapes differ from another's, so an id from the wrong family would
// be billed and then fail mid-stream.
var bedrockFamilies = map[bool]*regexp.Regexp{
	false: regexp.MustCompile(`(^|[./])anthropic\.`),
	true:  regexp.MustCompile(`(^|[./])xai\.`),
}

// Bedrock sends a request to Bedrock's runtime, signed with SigV4. With XAI set
// it serves a Chat Completions model, whose body it leaves as it is; otherwise it
// serves an Anthropic model, whose body only loses the two fields the address
// carries instead. The credentials are the account's own static keys: the secret
// is the stored key, and the key id and optional session token come from its
// provider specific data.
type Bedrock struct {
	Default *executor.Default
	XAI     bool
}

// Execute sends one call. req.Key holds the secret access key.
func (b *Bedrock) Execute(ctx context.Context, req executor.Request) (executor.Response, error) {
	if req.Key == "" {
		return executor.Response{}, executor.ErrNoCredential
	}
	cred, err := b.credentials(req)
	if err != nil {
		return executor.Response{}, err
	}
	if !bedrockFamilies[b.XAI].MatchString(strings.ToLower(req.Model)) {
		return executor.Response{}, executor.ErrBadAccount
	}
	req.URL = bedrockAddress(cred.region, req.Model, req.Stream)
	req.Body = b.body(req)
	req.NoRedirect = true
	req.Sign = func(method, target string, h http.Header, body []byte) error {
		_, err := sigv4.Sign(sigv4.Request{
			Method: method, URL: target, Header: h, Body: body,
			Region: cred.region, Service: bedrockService, AccessKey: cred.keyID,
			SecretKey: cred.secret, SessionToken: cred.token, Now: b.now(),
		})
		return err
	}
	// The credential is the signature, so the auth block is replaced rather than
	// inherited: a format's default would put the secret in a plain header.
	accept := "application/json"
	if req.Stream {
		accept = bedrockStreamAccept
	}
	req.Auth = &upstream.Auth{Headers: map[string]string{"Accept": accept}}
	return b.Default.Execute(ctx, req)
}

// bedrockCredential is the account's own AWS credential and where it signs for.
type bedrockCredential struct {
	keyID, secret, token, region string
}

// credentials reads the key pair and the region. The region lands in a
// hostname, so it is held to the shape of one before anything is sent.
func (b *Bedrock) credentials(req executor.Request) (bedrockCredential, error) {
	c := bedrockCredential{secret: req.Key, region: bedrockDefaultRegion}
	if req.Account == nil {
		return bedrockCredential{}, executor.ErrBadAccount
	}
	c.keyID = strings.TrimSpace(req.Account("accessKeyId"))
	c.token = strings.TrimSpace(req.Account("sessionToken"))
	if r := strings.TrimSpace(req.Account("region")); r != "" {
		c.region = r
	} else if e := strings.TrimSpace(req.Entry.Transport.Region); e != "" {
		c.region = e
	}
	if c.keyID == "" || !upstream.ValidRegion(c.region) {
		return bedrockCredential{}, executor.ErrBadAccount
	}
	// A temporary key is meaningless without its session token, and the failure
	// it earns upstream says nothing a reader can act on.
	if strings.HasPrefix(c.keyID, "ASIA") && c.token == "" {
		return bedrockCredential{}, executor.ErrBadAccount
	}
	return c, nil
}

func (b *Bedrock) now() func() time.Time { return time.Now }

// bedrockAddress is the model's endpoint. The id is escaped once here and a
// second time by the signer, which is what a ":0" version suffix needs.
func bedrockAddress(region, model string, stream bool) string {
	action := bedrockInvoke
	if stream {
		action = bedrockInvokeStream
	}
	return bedrockHost + region + bedrockSuffix + sigv4.Escape(model) + action
}

// body drops what the address already says and, on the Anthropic wire, pins the
// version. The pin goes in last so a client's own value cannot win it.
func (b *Bedrock) body(req executor.Request) []byte {
	out, err := sjson.DeleteBytes(req.Body, "model")
	if err != nil {
		return req.Body
	}
	if out, err = sjson.DeleteBytes(out, "stream"); err != nil {
		return req.Body
	}
	if b.XAI {
		return out
	}
	if out, err = sjson.SetBytes(out, "anthropic_version", bedrockAnthropicVersion); err != nil {
		return req.Body
	}
	return out
}
