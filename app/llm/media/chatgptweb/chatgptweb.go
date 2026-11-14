// Package chatgptweb generates an image through ChatGPT's web conversation:
// a session token traded from the pasted cookie, the Sentinel challenge, one
// picture_v2 conversation, a poll for the produced file and a download. It is
// the plain-HTTP path only; a Turnstile demand is answered with a refusal.
package chatgptweb

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"

	"rllmx/app/llm/upstream"
)

// The identity the web client presents. Every value is a fixed word: nothing
// about the machine or the operator goes on the wire.
const (
	userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/149.0.0.0 Safari/537.36"

	sentinelPrepare = "OpenAI-Sentinel-Chat-Requirements-Prepare-Token"
	sentinelToken   = "OpenAI-Sentinel-Chat-Requirements-Token"
	sentinelProof   = "OpenAI-Sentinel-Proof-Token"

	maxPrompt  = 8000
	maxSSE     = 16 << 20
	maxDoc     = 32 << 20
	maxImage   = 32 << 20
	maxPowIter = 500000
)

// Hop deadlines, in the reference's order of magnitude.
const (
	sessionBudget  = 15 * time.Second
	sentinelBudget = 15 * time.Second
	prepareBudget  = 60 * time.Second
	sseBudget      = 90 * time.Second
	pollBudget     = 240 * time.Second
	pollInterval   = 5 * time.Second
	fetchBudget    = 60 * time.Second
)

// Images is the image adapter for the provider.
type Images struct{}

// Kind is the service this adapter serves.
func (Images) Kind() string { return upstream.KindImage }

// Direct runs the whole exchange: the session, the challenge, the conversation,
// the poll and the download.
func (Images) Direct(ctx context.Context, r media.Request, c media.Client) (media.Response, error) {
	if c == nil {
		return media.Response{}, errors.New("chatgpt-web: no client")
	}
	prompt := strings.TrimSpace(gjson.GetBytes(r.Body, "prompt").String())
	if prompt == "" || len(prompt) > maxPrompt {
		return media.Response{}, media.BadRequestf("the prompt is empty or too long")
	}
	if n := gjson.GetBytes(r.Body, "n"); n.Exists() && n.Int() != 1 {
		return media.Response{}, media.BadRequestf("this provider makes one image per request")
	}
	jar, err := media.ParseCookie(r.Credential)
	if err != nil {
		return media.Response{}, &media.ProviderError{Status: http.StatusUnauthorized, Text: "The stored web cookie is not one this program sends"}
	}
	base := strings.TrimRight(media.BaseURL(r), "/")
	if base == "" {
		return media.Response{}, errors.New("chatgpt-web: no address")
	}
	size := gjson.GetBytes(r.Body, "size").String()
	text := prompt
	if size != "" {
		text = fmt.Sprintf("%s (aspect ratio %s)", prompt, media.SizeToAspectRatio(size))
	}

	token, err := session(ctx, c, base, jar.Header())
	if err != nil {
		return media.Response{}, err
	}
	sentinel, err := challenge(ctx, c, base, token)
	if err != nil {
		return media.Response{}, err
	}
	convID, files, sediments, err := converse(ctx, c, base, token, jar.Header(), sentinel, text)
	if err != nil {
		return media.Response{}, err
	}
	if len(files) == 0 && len(sediments) == 0 {
		if convID == "" {
			return media.Response{}, &media.ProviderError{Status: http.StatusBadGateway, Text: "ChatGPT produced no conversation"}
		}
		files, sediments, err = waitIDs(ctx, c, base, token, jar.Header(), convID)
		if err != nil {
			return media.Response{}, err
		}
	}
	b64, mime, err := download(ctx, c, base, token, jar.Header(), convID, files, sediments)
	if err != nil {
		return media.Response{}, err
	}
	out, err := json.Marshal(map[string]any{
		"created": media.NowSec(),
		"data":    []map[string]string{{"b64_json": b64, "mime_type": mime}},
	})
	if err != nil {
		return media.Response{}, err
	}
	return media.Response{Status: http.StatusOK, Header: map[string]string{"Content-Type": "application/json"}, Body: out}, nil
}

// session trades the cookie for the web client's access token.
func session(ctx context.Context, c media.Client, base, cookie string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, sessionBudget)
	defer cancel()
	status, _, body, err := call(cctx, c, http.MethodGet, base+"/api/auth/session", map[string]string{
		"Accept": "application/json", "Cookie": cookie, "User-Agent": userAgent,
	}, nil, maxDoc)
	if err != nil {
		return "", err
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return "", &media.ProviderError{Status: status, Text: "The ChatGPT session is not signed in"}
	}
	if status < 200 || status > 299 {
		return "", &media.ProviderError{Status: status, Text: "The ChatGPT session could not be read"}
	}
	token := gjson.GetBytes(body, "accessToken").String()
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return "", &media.ProviderError{Status: http.StatusUnauthorized, Text: "The ChatGPT session is not signed in"}
	}
	return token, nil
}

// challenge runs the Sentinel prepare and requirements steps and returns the
// headers a conversation call must carry.
func challenge(ctx context.Context, c media.Client, base, token string) (map[string]string, error) {
	cctx, cancel := context.WithTimeout(ctx, sentinelBudget)
	defer cancel()
	auth := map[string]string{"Authorization": "Bearer " + token, "User-Agent": userAgent}
	prepared, err := postJSON(cctx, c, base+"/backend-api/sentinel/chat-requirements/prepare",
		withHeader(auth, sentinelPrepare, prepareToken()), nil)
	if err != nil {
		return nil, err
	}
	prepare := gjson.GetBytes(prepared, "prepare_token").String()
	if prepare == "" {
		return nil, &media.ProviderError{Status: http.StatusBadGateway, Text: "ChatGPT sent no challenge token"}
	}
	req, err := postJSON(cctx, c, base+"/backend-api/sentinel/chat-requirements",
		withHeader(auth, sentinelPrepare, prepare), nil)
	if err != nil {
		return nil, err
	}
	h := map[string]string{}
	if v := gjson.GetBytes(req, "token").String(); v != "" {
		h[sentinelToken] = v
	}
	if gjson.GetBytes(req, "proofofwork.required").Bool() {
		proof, err := solve(gjson.GetBytes(req, "proofofwork.seed").String(), gjson.GetBytes(req, "proofofwork.difficulty").String())
		if err != nil {
			return nil, err
		}
		h[sentinelProof] = proof
	}
	if gjson.GetBytes(req, "turnstile.required").Bool() && len(h) == 0 {
		return nil, &media.ProviderError{Status: http.StatusBadGateway, Text: "ChatGPT demands a Turnstile challenge this router cannot solve"}
	}
	return h, nil
}

// converse prepares and runs one picture_v2 conversation, returning its id and
// any file or sediment ids the answer already named.
func converse(ctx context.Context, c media.Client, base, token, cookie string, sentinel map[string]string, text string) (string, []string, []string, error) {
	h := map[string]string{
		"Authorization": "Bearer " + token, "Cookie": cookie, "User-Agent": userAgent,
		"Content-Type": "application/json",
	}
	for k, v := range sentinel {
		h[k] = v
	}

	cctx, cancel := context.WithTimeout(ctx, prepareBudget)
	defer cancel()
	prepareBody := map[string]any{
		"action": "next", "fork_from_shared_post": false, "parent_message_id": media.UUID(),
		"model": "auto", "client_prepare_state": "success", "timezone_offset_min": 0,
		"timezone": "UTC", "conversation_mode": map[string]string{"kind": "primary_assistant"},
		"system_hints": []string{"picture_v2"},
		"partial_query": map[string]any{
			"id": media.UUID(), "author": map[string]string{"role": "user"},
			"content": map[string]any{"content_type": "text", "parts": []string{text}},
		},
		"supports_buffering": true, "supported_encodings": []string{"v1"},
		"client_contextual_info": map[string]string{"app_name": "chatgpt.com"},
	}
	raw, err := json.Marshal(prepareBody)
	if err != nil {
		return "", nil, nil, err
	}
	prepared, err := postJSON(cctx, c, base+"/backend-api/f/conversation/prepare", h, raw)
	if err != nil {
		return "", nil, nil, err
	}
	conduit := gjson.GetBytes(prepared, "conduit_token").String()

	conv := map[string]any{
		"action": "next",
		"messages": []map[string]any{{
			"id": media.UUID(), "author": map[string]string{"role": "user"},
			"create_time": float64(time.Now().UnixNano()) / 1e9,
			"content":     map[string]any{"content_type": "text", "parts": []string{text}},
			"metadata": map[string]any{
				"developer_mode_connector_ids": []string{}, "selected_github_repos": []string{},
				"selected_all_github_repos": false, "system_hints": []string{"picture_v2"},
				"serialization_metadata": map[string]any{"custom_symbol_offsets": []int{}},
			},
		}},
		"parent_message_id": media.UUID(), "model": "auto", "client_prepare_state": "sent",
		"timezone_offset_min": 0, "timezone": "UTC",
		"conversation_mode":        map[string]string{"kind": "primary_assistant"},
		"enable_message_followups": true, "system_hints": []string{"picture_v2"},
		"supports_buffering": true, "supported_encodings": []string{"v1"},
		"client_contextual_info": map[string]any{
			"is_dark_mode": false, "time_since_loaded": 1200, "page_height": 1072,
			"page_width": 1724, "pixel_ratio": 1.2, "screen_height": 1440,
			"screen_width": 2560, "app_name": "chatgpt.com",
		},
		"paragen_cot_summary_display_override": "allow", "force_parallel_switch": "auto",
	}
	raw, err = json.Marshal(conv)
	if err != nil {
		return "", nil, nil, err
	}
	h["Accept"] = "text/event-stream"
	h["X-Conduit-Token"] = conduit
	h["X-Oai-Turn-Trace-Id"] = media.UUID()

	sctx, scancel := context.WithTimeout(ctx, sseBudget)
	defer scancel()
	status, _, body, err := call(sctx, c, http.MethodPost, base+"/backend-api/f/conversation", h, raw, maxSSE)
	if err != nil {
		return "", nil, nil, err
	}
	if err := refuse(status, body); err != nil {
		return "", nil, nil, err
	}
	convID, files, sediments := scrape(body)
	return convID, files, sediments, nil
}

// waitIDs polls the conversation document until it lists a produced file.
func waitIDs(ctx context.Context, c media.Client, base, token, cookie, convID string) ([]string, []string, error) {
	select {
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	case <-time.After(pollInterval):
	}
	deadline := time.NewTimer(pollBudget)
	defer deadline.Stop()
	for {
		_, files, sediments, err := document(ctx, c, base, token, cookie, convID)
		if err != nil {
			var br *media.BadRequest
			if errors.As(err, &br) {
				return nil, nil, err
			}
		}
		if len(files) > 0 || len(sediments) > 0 {
			return files, sediments, nil
		}
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-deadline.C:
			return nil, nil, &media.ProviderError{Status: http.StatusGatewayTimeout, Text: "ChatGPT did not finish the image in time"}
		case <-time.After(pollInterval):
		}
	}
}

// document reads one conversation document under the cap and scrapes it.
func document(ctx context.Context, c media.Client, base, token, cookie, convID string) (string, []string, []string, error) {
	cctx, cancel := context.WithTimeout(ctx, fetchBudget)
	defer cancel()
	status, _, body, err := call(cctx, c, http.MethodGet, base+"/backend-api/conversation/"+convID, map[string]string{
		"Accept": "application/json", "Authorization": "Bearer " + token,
		"Cookie": cookie, "User-Agent": userAgent,
	}, nil, maxDoc)
	if err != nil {
		return "", nil, nil, err
	}
	if status < 200 || status > 299 {
		return "", nil, nil, &media.ProviderError{Status: http.StatusBadGateway, Text: "ChatGPT did not return the conversation"}
	}
	_, files, sediments := scrape(body)
	if len(files) == 0 && len(sediments) == 0 && contentPolicy(body) {
		return "", nil, nil, media.BadRequestf("the request was refused")
	}
	return convID, files, sediments, nil
}

// download resolves the first produced id to an image and its media type.
func download(ctx context.Context, c media.Client, base, token, cookie, convID string, files, sediments []string) (string, string, error) {
	for _, id := range files {
		target, err := resolveDownload(ctx, c, base+"/backend-api/files/"+id+"/download", token, cookie)
		if err != nil || target == "" {
			continue
		}
		if b64, mime, ok := fetchImage(ctx, c, target); ok {
			return b64, mime, nil
		}
	}
	for _, id := range sediments {
		target, err := resolveDownload(ctx, c, base+"/backend-api/conversation/"+convID+"/attachment/"+id+"/download", token, cookie)
		if err != nil || target == "" {
			continue
		}
		if b64, mime, ok := fetchImage(ctx, c, target); ok {
			return b64, mime, nil
		}
	}
	return "", "", &media.ProviderError{Status: http.StatusBadGateway, Text: "ChatGPT produced no downloadable file"}
}

// resolveDownload reads the asset address a file or attachment id answers.
func resolveDownload(ctx context.Context, c media.Client, target, token, cookie string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, fetchBudget)
	defer cancel()
	status, _, body, err := call(cctx, c, http.MethodGet, target, map[string]string{
		"Accept": "application/json", "Authorization": "Bearer " + token,
		"Cookie": cookie, "User-Agent": userAgent,
	}, nil, 1<<20)
	if err != nil || status < 200 || status > 299 {
		return "", err
	}
	url := gjson.GetBytes(body, "download_url").String()
	if url == "" {
		url = gjson.GetBytes(body, "url").String()
	}
	if url == "" || !strings.HasPrefix(url, "https://") {
		return "", nil
	}
	return url, nil
}

// fetchImage reads an asset the service already produced.
func fetchImage(ctx context.Context, c media.Client, target string) (string, string, bool) {
	cctx, cancel := context.WithTimeout(ctx, fetchBudget+30*time.Second)
	defer cancel()
	status, _, body, err := call(cctx, c, http.MethodGet, target, map[string]string{"User-Agent": userAgent}, nil, maxImage)
	if err != nil || status < 200 || status > 299 {
		return "", "", false
	}
	b64, mime := media.BinaryImage(body)
	return b64, mime, mime != ""
}

// refuse maps a refusal status to the client's answer.
func refuse(status int, body []byte) error {
	switch {
	case status >= 200 && status <= 299:
		return nil
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		if turnstile(body) {
			return &media.ProviderError{Status: status, Text: "ChatGPT demands a Turnstile challenge this router cannot solve"}
		}
		return &media.ProviderError{Status: status, Text: "The ChatGPT session is not signed in"}
	case status == http.StatusTooManyRequests:
		return &media.ProviderError{Status: status, Text: "ChatGPT has hit its image generation quota"}
	case status >= 400 && status < 500:
		return media.BadRequestf("the request was refused")
	}
	return &media.ProviderError{Status: http.StatusBadGateway, Text: "ChatGPT did not answer"}
}

// contentPolicy reports a refusal the conversation document spells out.
func contentPolicy(body []byte) bool {
	text := strings.ToLower(string(body))
	return strings.Contains(text, "is_error") && strings.Contains(text, "policy")
}

// turnstile reports a challenge in an answer.
func turnstile(body []byte) bool { return strings.Contains(strings.ToLower(string(body)), "turnstile") }

// postJSON sends one JSON call and returns its body.
func postJSON(ctx context.Context, c media.Client, target string, h map[string]string, body []byte) ([]byte, error) {
	if body == nil {
		body = []byte("{}")
	}
	h["Content-Type"] = "application/json"
	status, _, raw, err := call(ctx, c, http.MethodPost, target, h, body, 1<<20)
	if err != nil {
		return nil, err
	}
	if status < 200 || status > 299 {
		if turnstile(raw) {
			return nil, &media.ProviderError{Status: status, Text: "ChatGPT demands a Turnstile challenge this router cannot solve"}
		}
		return nil, &media.ProviderError{Status: status, Text: "ChatGPT refused the challenge"}
	}
	return raw, nil
}

// call sends one request, reading the answer under limit when the client can
// cap it.
func call(ctx context.Context, c media.Client, method, target string, h map[string]string, body []byte, limit int64) (int, http.Header, []byte, error) {
	if capped, ok := c.(interface {
		DoCapped(ctx context.Context, method, target string, header map[string]string, body []byte, limit int64) (int, http.Header, []byte, error)
	}); ok {
		return capped.DoCapped(ctx, method, target, h, body, limit)
	}
	status, header, raw, err := c.Do(ctx, method, target, h, body)
	if err != nil {
		return 0, nil, nil, err
	}
	if int64(len(raw)) > limit {
		return 0, nil, nil, &media.ProviderError{Status: http.StatusBadGateway, Text: "ChatGPT answered more than this router reads"}
	}
	return status, header, raw, nil
}

// withHeader returns h plus one value.
func withHeader(h map[string]string, name, value string) map[string]string {
	out := make(map[string]string, len(h)+1)
	for k, v := range h {
		out[k] = v
	}
	out[name] = value
	return out
}

// The scraper reads ids and the conversation id out of an answer.
var (
	convPattern     = regexp.MustCompile(`"conversation_id"\s*:\s*"([0-9a-f-]{36})"`)
	filePattern     = regexp.MustCompile(`file-service://([A-Za-z0-9_-]+)`)
	fileIDPattern   = regexp.MustCompile(`\bfile_00000000[0-9a-f]{24}\b`)
	sedimentPattern = regexp.MustCompile(`sediment://([A-Za-z0-9_-]+)`)
)

const maxIDs = 8

// scrape returns the conversation id and the distinct file and sediment ids.
func scrape(body []byte) (string, []string, []string) {
	conv := ""
	if m := convPattern.FindSubmatch(body); len(m) == 2 {
		conv = string(m[1])
	}
	files := collect(body, filePattern, fileIDPattern)
	sediments := collect(body, sedimentPattern, nil)
	return conv, files, sediments
}

// collect gathers distinct matches of one or two patterns, bounded.
func collect(body []byte, first, second *regexp.Regexp) []string {
	seen := map[string]bool{}
	var out []string
	add := func(v string) {
		if len(out) >= maxIDs || v == "" || v == "file_upload" || seen[v] {
			return
		}
		seen[v] = true
		out = append(out, v)
	}
	for _, m := range first.FindAllSubmatch(body, maxIDs) {
		if len(m) == 2 {
			add(string(m[1]))
		}
	}
	if second != nil {
		for _, m := range second.FindAll(body, maxIDs) {
			add(string(m))
		}
	}
	return out
}

// prepareToken is the 24-slot browser fingerprint the bundle hashes, with the
// counter slot set to one.
func prepareToken() string {
	slots := fingerprint()
	slots[3] = 1
	slots[9] = 0
	raw, _ := json.Marshal(slots)
	return "gAAAAAC" + base64.StdEncoding.EncodeToString(raw)
}

// solve brute-forces the proof-of-work counter the service asked for.
func solve(seed, difficulty string) (string, error) {
	if seed == "" || difficulty == "" {
		return "", &media.ProviderError{Status: http.StatusBadGateway, Text: "ChatGPT asked for proof of work without a seed or difficulty"}
	}
	slots := fingerprint()
	started := time.Now()
	for counter := 0; counter < maxPowIter; counter++ {
		slots[3] = counter
		slots[9] = int(time.Since(started).Milliseconds())
		raw, _ := json.Marshal(slots)
		encoded := base64.StdEncoding.EncodeToString(raw)
		h := hash(seed + encoded)
		if len(difficulty) <= len(h) && h[:len(difficulty)] <= difficulty {
			return "gAAAAAB" + encoded + "~S", nil
		}
	}
	return "", &media.ProviderError{Status: http.StatusBadGateway, Text: "ChatGPT's proof of work did not finish"}
}

// hash is the challenge's 32-bit hash: FNV-1a with three avalanche rounds,
// written as eight hex digits.
func hash(s string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	v := h.Sum32()
	v ^= v >> 16
	v *= 0x85ebca6b
	v ^= v >> 13
	v *= 0xc2b2ae35
	v ^= v >> 16
	return fmt.Sprintf("%08x", v)
}

// fingerprint builds the slots the bundle hashes. The service checks the shape,
// not the values, so fixed plausible ones are enough.
func fingerprint() []any {
	slots := make([]any, 25)
	slots[0] = 2080
	slots[1] = time.Now().Format("Mon Jan 2 2006 15:04:05 GMT-0700 (MST)")
	slots[2] = nil
	slots[3] = 0
	slots[4] = userAgent
	slots[5] = "https://chatgpt.com/cdn/assets/685809.dec86dcdc1.js"
	slots[6] = nil
	slots[7] = "en-US"
	slots[8] = "en-US,en"
	slots[9] = 0
	slots[10] = "userAgent−" + userAgent
	slots[11] = "cookie"
	slots[12] = "caches"
	slots[13] = 0
	slots[14] = media.UUID()
	slots[15] = ""
	slots[16] = 8
	slots[17] = 0
	for i := 18; i < 25; i++ {
		slots[i] = 0
	}
	return slots
}

var _ media.Director = Images{}
