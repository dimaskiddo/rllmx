package deepseek

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"rllmx/app/llm/executor"
	"rllmx/app/llm/upstream"
	"rllmx/app/protocol/keccak"
	"rllmx/tests"
)

// fakeClient sends every address to the fake server, so the provider's real
// host is never dialled and the executor's https check still sees its own URL.
func fakeClient(up *tests.Upstream) *http.Client {
	return &http.Client{Transport: tests.Redirect(func(r *http.Request) {
		r.URL.Scheme, r.URL.Host = "http", strings.TrimPrefix(up.URL, "http://")
	})}
}

// challengeFor builds a real challenge whose answer is the given nonce.
func challengeFor(t *testing.T, nonce int) map[string]any {
	t.Helper()
	const salt = "SALT-1"
	const expire = int64(1700000000)
	sum := keccak.Sum256([]byte(salt+"_"+strconv.FormatInt(expire, 10)+"_"+strconv.Itoa(nonce)), 1)
	return map[string]any{
		"algorithm": "DeepSeekHashV1", "challenge": hex.EncodeToString(sum[:]),
		"salt": salt, "signature": "sig-1", "target_path": "/api/v0/chat/completion",
		"difficulty": nonce + 10, "expire_at": expire,
	}
}

// script is the fake service's answers, in order.
func script(t *testing.T, nonce int) []tests.Reply {
	t.Helper()
	challenge, _ := json.Marshal(map[string]any{"code": 0, "data": map[string]any{"biz_data": map[string]any{"challenge": challengeFor(t, nonce)}}})
	return []tests.Reply{
		{ContentType: "application/json", Body: `{"code":0,"data":{"biz_data":{"token":"ACCESS-1"}}}`},
		{ContentType: "application/json", Body: `{"code":0,"data":{"biz_data":{"chat_session":{"id":"sess-1"}}}}`},
		{ContentType: "application/json", Body: string(challenge)},
		{ContentType: "text/event-stream", Chunks: []string{
			`data: {"p":"response/fragments","v":[{"type":"THINK","content":"plan "}]}` + "\n\n",
			`data: {"p":"response/fragments","v":[{"type":"ANSWER","content":"Hello"}]}` + "\n\n",
			`data: {"p":"response/status","v":"FINISHED"}` + "\n\n",
		}},
		{ContentType: "application/json", Body: `{"code":0}`},
	}
}

func request(up *tests.Upstream) executor.Request {
	return executor.Request{
		URL:   "https://chat.deepseek.com/api/v0/chat/completion",
		Body:  []byte(`{"model":"deepseek-chat","messages":[{"role":"user","content":"hi"}]}`),
		Key:   "USER-TOKEN",
		Model: "deepseek-chat",
		Entry: upstream.Entry{},
	}
}

func TestDeepseekSendsTheTurnInOrder(t *testing.T) {
	up := tests.NewUpstream(t, script(t, 7)...)
	e := &Executor{Default: &executor.Default{Client: fakeClient(up)}, Cache: nil}
	resp, err := e.Execute(context.Background(), request(up))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	out, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(out), `"content":"Hello"`) || !strings.Contains(string(out), `"reasoning_content":"plan "`) {
		t.Fatalf("answer = %s", out)
	}
	if !strings.Contains(string(out), `"finish_reason":"stop"`) || !strings.Contains(string(out), "data: [DONE]") {
		t.Fatalf("the turn did not end: %s", out)
	}
	calls := up.Calls()
	var paths []string
	for _, c := range calls {
		paths = append(paths, c.Path)
	}
	want := []string{
		"/api/v0/users/current", "/api/v0/chat_session/create", "/api/v0/chat/create_pow_challenge",
		"/api/v0/chat/completion", "/api/v0/chat_session/delete",
	}
	if strings.Join(paths, " ") != strings.Join(want, " ") {
		t.Fatalf("calls = %v", paths)
	}
	if got := calls[0].Header.Get("Authorization"); got != "Bearer USER-TOKEN" {
		t.Fatalf("token call auth = %q", got)
	}
	if got := calls[3].Header.Get("Authorization"); got != "Bearer ACCESS-1" {
		t.Fatalf("completion auth = %q", got)
	}
	if calls[3].Header.Get(powHeader) == "" {
		t.Fatalf("the completion carried no proof")
	}
	if calls[3].Header.Get("X-Client-Platform") != clientPlat || calls[3].Header.Get("User-Agent") != clientAgent {
		t.Fatalf("identity headers = %v", calls[3].Header)
	}
	var sent struct {
		ChatSessionID string `json:"chat_session_id"`
		Prompt        string `json:"prompt"`
		ModelType     string `json:"model_type"`
		Thinking      bool   `json:"thinking_enabled"`
		Search        bool   `json:"search_enabled"`
	}
	if err := json.Unmarshal(calls[3].Body, &sent); err != nil {
		t.Fatalf("completion body: %v", err)
	}
	if sent.ChatSessionID != "sess-1" || sent.Prompt != "hi" || sent.ModelType != "default" {
		t.Fatalf("completion body = %+v", sent)
	}
}

func TestTheProofOfWorksAnswerIsTheNonce(t *testing.T) {
	up := tests.NewUpstream(t, script(t, 7)...)
	e := &Executor{Default: &executor.Default{Client: fakeClient(up)}}
	resp, err := e.Execute(context.Background(), request(up))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	_ = resp.Body.Close()
	raw := up.Calls()[3].Header.Get(powHeader)
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		t.Fatalf("proof is not base64: %v", err)
	}
	var proof struct {
		Algorithm string `json:"algorithm"`
		Answer    int    `json:"answer"`
		Signature string `json:"signature"`
	}
	if err := json.Unmarshal(decoded, &proof); err != nil {
		t.Fatalf("proof is not JSON: %v", err)
	}
	if proof.Answer != 7 || proof.Algorithm != "DeepSeekHashV1" || proof.Signature != "sig-1" {
		t.Fatalf("proof = %+v", proof)
	}
}

func TestARejectedUserTokenIsAnAccountProblem(t *testing.T) {
	up := tests.NewUpstream(t, tests.Reply{Status: http.StatusUnauthorized, Body: `{}`})
	e := &Executor{Default: &executor.Default{Client: fakeClient(up)}}
	if _, err := e.Execute(context.Background(), request(up)); err != executor.ErrBadAccount {
		t.Fatalf("err = %v, want ErrBadAccount", err)
	}
	if n := len(up.Calls()); n != 1 {
		t.Fatalf("%d calls, want 1", n)
	}
}

func TestACodeCarriedInA200BecomesItsStatus(t *testing.T) {
	challenge, _ := json.Marshal(map[string]any{"code": 0, "data": map[string]any{"biz_data": map[string]any{"challenge": challengeFor(t, 0)}}})
	up := tests.NewUpstream(t,
		tests.Reply{Body: `{"code":0,"data":{"biz_data":{"token":"A"}}}`},
		tests.Reply{Body: `{"code":0,"data":{"biz_data":{"chat_session":{"id":"s"}}}}`},
		tests.Reply{ContentType: "application/json", Body: string(challenge)},
		tests.Reply{ContentType: "application/json", Body: `{"code":40003,"msg":"token expired"}`},
		tests.Reply{Body: `{"code":0}`},
	)
	e := &Executor{Default: &executor.Default{Client: fakeClient(up)}}
	resp, err := e.Execute(context.Background(), request(up))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	_ = resp.Body.Close()
	if resp.Status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.Status)
	}
}

func TestAMalformedChallengeIsNotSearched(t *testing.T) {
	up := tests.NewUpstream(t,
		tests.Reply{Body: `{"code":0,"data":{"biz_data":{"token":"A"}}}`},
		tests.Reply{Body: `{"code":0,"data":{"biz_data":{"chat_session":{"id":"s"}}}}`},
		tests.Reply{Body: `{"code":0,"data":{"biz_data":{"challenge":{"algorithm":"DeepSeekHashV1","challenge":"zz","salt":"s","signature":"x","target_path":"/p","difficulty":1,"expire_at":1}}}}`},
	)
	e := &Executor{Default: &executor.Default{Client: fakeClient(up)}}
	if _, err := e.Execute(context.Background(), request(up)); err == nil {
		t.Fatal("a malformed challenge was accepted")
	}
	if n := len(up.Calls()); n != 3 {
		t.Fatalf("%d calls: the completion must not be attempted", n)
	}
}
