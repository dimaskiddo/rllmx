package deepseek

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/executor"
	"rllmx/tests"
)

const toolBody = `{"model":"deepseek-chat","messages":[{"role":"user","content":"read the file"}],
"tools":[{"type":"function","function":{"name":"read","description":"read a file","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}}]}`

func TestToolManifestNamesTheBindingAndTheTools(t *testing.T) {
	manifest := toolManifest(gjson.Get(toolBody, "tools"), "abc12345")
	for _, want := range []string{`"_nonce": "abc12345"`, "- read: read a file", `{"type":"object"`, "Available tools:"} {
		if !strings.Contains(manifest, want) {
			t.Errorf("manifest is missing %q:\n%s", want, manifest)
		}
	}
	if toolManifest(gjson.Parse(`[]`), "x") != "" {
		t.Error("an empty tool list produced a manifest")
	}
	if toolManifest(gjson.Get(toolBody, "tools"), "") != "" {
		t.Error("a missing nonce produced a manifest")
	}
}

func TestToolConversationReplaysTheTrajectory(t *testing.T) {
	body := gjson.Parse(`{"messages":[
		{"role":"system","content":"be brief"},
		{"role":"user","content":"read a"},
		{"role":"assistant","content":"","tool_calls":[{"id":"c1","function":{"name":"read","arguments":"{\"path\":\"a\"}"}}]},
		{"role":"tool","tool_call_id":"c1","content":"done"},
		{"role":"user","content":"now b"}]}`)
	got := toolConversation(body.Get("messages").Array(), "MANIFEST")
	for _, want := range []string{"MANIFEST", "be brief", "User: read a", `<tool>{"name": "read", "arguments": {"path":"a"}}</tool>`,
		"Tool result (read): done", "User: now b", "Continue the task"} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt is missing %q:\n%s", want, got)
		}
	}
}

func TestParseCallsTakesOnlyACorrectBlock(t *testing.T) {
	offered := map[string]bool{"read": true}
	cases := []struct {
		name  string
		text  string
		calls int
		kept  string
	}{
		{"canonical", `<tool>{"name":"read","arguments":{"path":"a"},"_nonce":"N"}</tool>`, 1, ""},
		{"with prose", "let me look\n<tool>{\"name\":\"read\",\"arguments\":{},\"_nonce\":\"N\"}</tool>\ndone", 1, "let me look\n\ndone"},
		{"two blocks", `<tool>{"name":"read","arguments":{},"_nonce":"N"}</tool><tool>{"name":"read","arguments":{"p":1},"_nonce":"N"}</tool>`, 2, ""},
		{"wrong nonce", `<tool>{"name":"read","arguments":{},"_nonce":"X"}</tool>`, 0, `<tool>{"name":"read","arguments":{},"_nonce":"X"}</tool>`},
		{"unknown tool", `<tool>{"name":"write","arguments":{},"_nonce":"N"}</tool>`, 0, `<tool>{"name":"write","arguments":{},"_nonce":"N"}</tool>`},
		{"arguments not an object", `<tool>{"name":"read","arguments":"x","_nonce":"N"}</tool>`, 0, `<tool>{"name":"read","arguments":"x","_nonce":"N"}</tool>`},
		{"unterminated", `<tool>{"name":"read"`, 0, `<tool>{"name":"read"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			calls, kept := parseCalls(c.text, "N", offered)
			if len(calls) != c.calls || kept != strings.TrimSpace(c.kept) {
				t.Fatalf("calls = %+v, kept = %q", calls, kept)
			}
		})
	}
}

func TestAToolAnswerBecomesToolCalls(t *testing.T) {
	var sawPrompt, sawAuth string
	challenge, _ := json.Marshal(map[string]any{"code": 0, "data": map[string]any{"biz_data": map[string]any{"challenge": challengeFor(t, 0)}}})
	noncePattern := regexp.MustCompile(`"_nonce": "([0-9a-f]+)"`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch {
		case strings.HasSuffix(r.URL.Path, "/users/current"):
			_, _ = w.Write([]byte(`{"code":0,"data":{"biz_data":{"token":"A"}}}`))
		case strings.HasSuffix(r.URL.Path, "/chat_session/create"):
			_, _ = w.Write([]byte(`{"code":0,"data":{"biz_data":{"chat_session":{"id":"s"}}}}`))
		case strings.HasSuffix(r.URL.Path, "/create_pow_challenge"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(challenge)
		case strings.HasSuffix(r.URL.Path, "/chat/completion"):
			sawAuth = r.Header.Get("Authorization")
			sawPrompt = gjson.GetBytes(body, "prompt").String()
			nonce := noncePattern.FindStringSubmatch(sawPrompt)
			if nonce == nil {
				t.Errorf("the prompt carried no nonce: %s", sawPrompt)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			block := `<tool>{"name":"read","arguments":{"path":"a"},"_nonce":"` + first(nonce) + `"}</tool>`
			_, _ = io.WriteString(w, `data: {"p":"response/fragments","v":[{"type":"ANSWER","content":`+quote(block)+`}]}`+"\n\n")
			_, _ = io.WriteString(w, `data: {"p":"response/status","v":"FINISHED"}`+"\n\n")
		default:
			_, _ = w.Write([]byte(`{"code":0}`))
		}
	}))
	defer srv.Close()
	client := &http.Client{Transport: tests.Redirect(func(r *http.Request) {
		r.URL.Scheme, r.URL.Host = "http", strings.TrimPrefix(srv.URL, "http://")
	})}
	e := &Executor{Default: &executor.Default{Client: client}}
	resp, err := e.Execute(context.Background(), withTools(request(nil)))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	out, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if sawAuth != "Bearer A" {
		t.Fatalf("completion auth = %q", sawAuth)
	}
	for _, want := range []string{`"tool_calls":[{"index":0,"id":"call_read_0","type":"function","function":{"name":"read","arguments":"{\"path\":\"a\"}"}}]`,
		`"finish_reason":"tool_calls"`, "data: [DONE]"} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("answer is missing %q; got %s", want, out)
		}
	}
	if strings.Contains(string(out), "<tool>") {
		t.Fatalf("the markup reached the client: %s", out)
	}
}

// withTools is the request carrying the tool body.
func withTools(r executor.Request) executor.Request {
	r.Body = []byte(toolBody)
	return r
}

// first returns the first match of a group or "".
func first(m []string) string {
	if len(m) < 2 {
		return ""
	}
	return m[1]
}
