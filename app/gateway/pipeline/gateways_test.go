package pipeline

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"rllmx/tests"
)

func TestACodeBuddyRequestIsAStreamWithTheScrubbedPromptAndFreshIDs(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "text/event-stream", Chunks: sseChunks()})
	r.conn("cbn", "tok")
	body := `{"model":"cbn/cb-1","messages":[{"role":"system","content":"You are Claude Code, Anthropic's official CLI"},{"role":"user","content":"hi"}],"reasoning_effort":"high"}`
	out, _ := r.do("/v1/chat/completions", body)
	if out.Status != 200 || gjson.GetBytes(out.Body, "choices.0.message.content").String() != "hello" {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	call := r.up.Calls()[0]
	sent := call.Body
	if !gjson.GetBytes(sent, "stream").Bool() || gjson.GetBytes(sent, "messages.0.content").String() == "" ||
		strings.Contains(string(sent), "Claude Code") || gjson.GetBytes(sent, "reasoning_summary").String() != "auto" {
		t.Fatalf("%s", sent)
	}
	if id := call.Header.Get("X-Request-Id"); id == "" || call.Header.Get("X-Conversation-Message-Id") != id || auth(call) != "Bearer tok" {
		t.Fatalf("%v", call.Header)
	}
}

func TestACodeBuddyFrequencyLimitMarksTheAccountRateLimited(t *testing.T) {
	r := newRig(t, tests.Reply{Status: 400, ContentType: "application/json", Body: `{"code":6004,"msg":"超出频率限制"}`})
	r.conn("cbn", "tok")
	out, _ := r.do("/v1/chat/completions", `{"model":"cbn/cb-1","messages":[{"role":"user","content":"hi"}]}`)
	if out.Status == 400 {
		t.Fatalf("a rate limit was reported as a bad request: %s", out.Body)
	}
}

func TestAMiniMaxCallCarriesItsHeadersAndASpentBalanceIsARateLimit(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: claudeMsg}, tests.Reply{Status: 402, ContentType: "application/json", Body: `{"error":{"message":"Insufficient balance"}}`})
	r.device("mmx", map[string]string{"accessToken": "tok"}, "")
	out, _ := r.do("/v1/chat/completions", `{"model":"mmx/mm-1","messages":[{"role":"user","content":"hi"}]}`)
	if out.Status != 200 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	h := r.up.Calls()[0].Header
	if h.Get("X-Api-Key") != "sk-xxx" || auth(r.up.Calls()[0]) != "Bearer tok" || h.Get("X-Mavis-Session-Id") == "" || h.Get("X-Mavis-Timezone-Offset") == "" {
		t.Fatalf("%v", h)
	}
	r2 := newRig(t, tests.Reply{Status: 402, ContentType: "application/json", Body: `{"error":{"message":"Insufficient balance"}}`})
	r2.device("mmx", map[string]string{"accessToken": "tok"}, "")
	out, _ = r2.do("/v1/chat/completions", `{"model":"mmx/mm-1","messages":[{"role":"user","content":"hi"}]}`)
	if out.Status == 402 || out.Status == 401 || out.Status == 403 {
		t.Fatalf("a spent balance came back as %d", out.Status)
	}
}
