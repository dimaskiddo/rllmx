package executor

import (
	"context"
	"regexp"
	"strings"

	"rllmx/app/dialect"
	"rllmx/app/upstream"
)

// Kiro's hosts and paths.
const (
	kiroPath       = "/generateAssistantResponse"
	kiroDefaultReg = "us-east-1"
	kiroTarget     = "AmazonCodeWhispererStreamingService.GenerateAssistantResponse"
)

// kiroFallbacks are the answers that mean this host does not serve the
// credential. A 400 is not one: the same body cannot fare better elsewhere.
var kiroFallbacks = map[int]bool{401: true, 403: true, 404: true}

// kiroRegion finds the region inside an amazonaws host, so an account's own
// region replaces the default one.
var kiroRegion = regexp.MustCompile(`([a-z]+)\.[a-z0-9-]+\.amazonaws\.com`)

// Kiro sends a request to CodeWhisperer, whose answer is framed in AWS
// EventStream and whose body the kiro codec built. It orders the hosts by how
// the account signed in and tries the next one only when the answer says the
// credential is not served here.
type Kiro struct {
	Default *Default
}

// Execute sends one call.
func (k *Kiro) Execute(ctx context.Context, req Request) (Response, error) {
	if req.Key == "" {
		return Response{}, ErrNoCredential
	}
	auth := kiroAuth{method: kiroMethod(req), token: req.Key}
	profile := ""
	if req.Account != nil {
		profile = strings.TrimSpace(req.Account("profileArn"))
	}
	hosts := kiroHosts(req.Entry, auth.method, kiroRegionOf(req))
	var resp Response
	var err error
	for _, host := range hosts {
		resp, err = k.send(ctx, req, host, auth, profile)
		if err != nil {
			return resp, err
		}
		if !kiroFallbacks[resp.Status] {
			break
		}
		// The body is drained rather than read: this host does not serve the
		// credential, so its answer says nothing about the next one.
		drain(resp.Body)
	}
	if err != nil || resp.Status < 200 || resp.Status > 299 || !req.Stream {
		return resp, err
	}
	return k.inspect(ctx, req, resp, auth, profile)
}

// send makes one call on one host.
func (k *Kiro) send(ctx context.Context, req Request, host string, auth kiroAuth, profile string) (Response, error) {
	req.URL = host + kiroPath
	req.NoRedirect = true
	req.Auth = &upstream.Auth{Headers: kiroHeaders(host, auth, profile)}
	return k.Default.Execute(ctx, req)
}

// inspect reads the answer whole and asks once more when the turn ended on
// nothing. A client that asked for JSON is left to the pipeline: the gate only
// judges a stream, because only a stream can be replayed.
func (k *Kiro) inspect(ctx context.Context, req Request, resp Response, auth kiroAuth, profile string) (Response, error) {
	if req.Account != nil && req.Account("kiroToolCallRepair") == "false" {
		return resp, nil
	}
	first, err := k.gate(ctx, resp, func() string { return "" })
	if err != nil {
		return Response{}, err
	}
	if first.verdict == kiroComplete || first.verdict == kiroBroken {
		return finishKiro(resp, first), nil
	}
	// The turn ended on nothing: ask once more, with the instruction that says
	// what was wrong. A second failure is answered as it came.
	retry := req
	retry.Body = appendKiroRepair(req.Body, first.reason)
	hosts := kiroHosts(req.Entry, auth.method, kiroRegionOf(req))
	retryResp, rerr := k.send(ctx, retry, hosts[0], auth, profile)
	if rerr != nil {
		return Response{}, rerr
	}
	if retryResp.Status < 200 || retryResp.Status > 299 {
		return retryResp, nil
	}
	second, serr := k.gate(ctx, retryResp, func() string { return first.reason })
	if serr != nil {
		return Response{}, serr
	}
	return finishKiro(retryResp, second), nil
}

// finishKiro turns a buffered answer into a body the pipeline reads: the events
// of a complete turn, or the events of a turn that could not be repaired.
func finishKiro(resp Response, a kiroAttempt) Response {
	if a.body != nil {
		return gateResponse(resp, a.body)
	}
	return replyWith(resp, httpStatus(resp), []byte(kiroFailureBody(a.reason)), "text/event-stream")
}

// kiroAuth is how the credential is presented: an API key announces its own
// kind, a login's token is simply a bearer.
type kiroAuth struct {
	method string
	token  string
}

// kiroMethod is the sign-in the account was made with.
func kiroMethod(req Request) string {
	if req.Account == nil {
		return "api_key"
	}
	m := strings.TrimSpace(req.Account("authMethod"))
	if m == "" {
		return "builder-id"
	}
	return m
}

// kiroRegionOf reads the account's region, and otherwise the provider's default.
func kiroRegionOf(req Request) string {
	r := ""
	if req.Account != nil {
		r = strings.TrimSpace(req.Account("region"))
	}
	if r == "" {
		r = strings.TrimSpace(req.Entry.Transport.Region)
	}
	if !upstream.ValidRegion(r) {
		return kiroDefaultReg
	}
	return r
}

// kiroHosts orders the provider's addresses: the Amazon Q host first for an API
// key, and the legacy kiro.dev host last for every method, since a modern body
// is refused there.
func kiroHosts(e upstream.Entry, method, region string) []string {
	var amazon, other []string
	for _, u := range e.Transport.BaseURLs {
		u = strings.TrimSuffix(u, kiroPath)
		if strings.Contains(u, ".amazonaws.com") {
			amazon = append(amazon, kiroRegional(u, region))
			continue
		}
		other = append(other, u)
	}
	if len(amazon) == 0 && len(other) == 0 {
		return []string{"https://q." + region + ".amazonaws.com"}
	}
	var q, rest []string
	for _, u := range amazon {
		if strings.Contains(u, "://q.") {
			q = append(q, u)
			continue
		}
		rest = append(rest, u)
	}
	if method == "api_key" {
		return append(append(q, rest...), other...)
	}
	return append(append(rest, other...), q...)
}

// kiroRegional moves an Amazon host onto the account's own region.
func kiroRegional(u, region string) string {
	if region == "" || region == kiroDefaultReg {
		return u
	}
	return kiroRegion.ReplaceAllString(u, "${1}."+region+".amazonaws.com")
}

// kiroHeaders are the identity headers of the client the account belongs to. A
// data file's own headers are merged underneath, so none of them is lost.
func kiroHeaders(host string, auth kiroAuth, profile string) map[string]string {
	h := map[string]string{
		"Accept":                          "application/vnd.amazon.eventstream",
		"Amz-Sdk-Request":                 "attempt=1; max=3",
		"Amz-Sdk-Invocation-Id":           dialect.NewID(),
		"x-amzn-kiro-agent-mode":          "spec",
		"x-amzn-codewhisperer-machine-id": "kiro-desktop",
		"Authorization":                   "Bearer " + auth.token,
		"x-amz-sso-bearer":                auth.token,
	}
	if strings.Contains(host, "://codewhisperer.") {
		h["X-Amz-Target"] = kiroTarget
	}
	switch auth.method {
	case "api_key":
		h["TokenType"] = "API_KEY"
	case "external_idp":
		h["TokenType"] = "EXTERNAL_IDP"
	}
	if profile != "" {
		h["x-amzn-codewhisperer-profile-arn"] = profile
	}
	return h
}
