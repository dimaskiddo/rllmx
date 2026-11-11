package cursor

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"

	"rllmx/app/llm/executor"
)

// serverFrames builds a turn: some deltas, an answer to a request the service
// makes, and the turn's end.
func serverFrames(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, frameBytes(p)...)
	}
	return out
}

// update is one interaction update with a text delta.
func update(field protowire.Number, s string) []byte {
	return message(serverInteraction, message(field, text(updateText, s)))
}

func TestTurnReaderEmitsTextThenEnds(t *testing.T) {
	body := serverFrames(
		update(updateText, "Hello"),
		update(updateText, " world"),
		message(serverInteraction, number(updateEnd, 0)),
	)
	r := &turnRead{frames: &frameReader{r: bytes.NewReader(body)}, model: "m", close: func() error { return nil }}
	out, err := drain(t, r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(out, `"content":"Hello"`) || !strings.Contains(out, `"content":" world"`) {
		t.Fatalf("answer = %s", out)
	}
	if !strings.Contains(out, `"finish_reason":"stop"`) || !strings.Contains(out, "data: [DONE]") {
		t.Fatalf("answer does not end the turn: %s", out)
	}
}

// drain reads a reader to its end.
func drain(t *testing.T, r interface{ Read([]byte) (int, error) }) (string, error) {
	t.Helper()
	var b strings.Builder
	buf := make([]byte, 4<<10)
	for {
		n, err := r.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			if err.Error() == "EOF" {
				return b.String(), nil
			}
			return b.String(), err
		}
	}
}

func TestTurnReaderAnswersAContextRequestOnTheSameStream(t *testing.T) {
	exec := message(serverExec, join(number(execID, 7), text(execRef, "ref-1"), message(execContext, nil)))
	body := serverFrames(exec,
		update(updateText, "done"),
		message(serverInteraction, number(updateEnd, 0)))
	var sent [][]byte
	r := &turnRead{frames: &frameReader{r: bytes.NewReader(body)}, model: "m",
		write: func(b []byte) error { sent = append(sent, b); return nil }, close: func() error { return nil }}
	if _, err := drain(t, r); err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(sent) != 1 {
		t.Fatalf("sent %d answers, want 1", len(sent))
	}
	payload := sent[0][frameHeader:]
	answer := field(t, payload, serverExec)
	if id, _ := readNumber(answer, execID); id != 7 {
		t.Fatalf("ack id = %d, want 7", id)
	}
	if readText(answer, execRef) != "ref-1" {
		t.Fatalf("ack ref = %q", readText(answer, execRef))
	}
	if _, ok := readMessage(answer, execContext); !ok {
		t.Fatal("the ack is not a request-context result")
	}
}

func TestTurnReaderTurnsAnMCPCallIntoAToolCall(t *testing.T) {
	args := message(mcpArgsEntry, join(text(mapKey, "path"), message(mapValue, text(valueString, "/tmp/x"))))
	call := message(serverExec, join(number(execID, 2), text(execRef, "r"),
		message(execMCPCall, join(text(mcpArgsName, "read"), text(mcpArgsCallID, "call-1"),
			text(mcpArgsToolName, "read_file"), message(mcpArgsEntry, nil), args))))
	body := serverFrames(call)
	r := &turnRead{frames: &frameReader{r: bytes.NewReader(body)}, model: "m",
		write: func([]byte) error { return nil }, close: func() error { return nil }}
	out, err := drain(t, r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(out, `"name":"read_file"`) || !strings.Contains(out, `"id":"call-1"`) {
		t.Fatalf("tool call = %s", out)
	}
	if !strings.Contains(out, `"finish_reason":"tool_calls"`) {
		t.Fatalf("the turn did not end on the call: %s", out)
	}
	var got struct {
		Choices []struct {
			Delta struct {
				ToolCalls []struct {
					Function struct {
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"delta"`
		} `json:"choices"`
	}
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "data: {") {
			continue
		}
		_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &got)
	}
	if len(got.Choices) == 0 || len(got.Choices[0].Delta.ToolCalls) == 0 {
		t.Fatalf("no tool call in %s", out)
	}
	if args := got.Choices[0].Delta.ToolCalls[0].Function.Arguments; args != `{"path":"/tmp/x"}` {
		t.Fatalf("arguments = %q", args)
	}
}

func TestTurnReaderReportsATrailerRefusal(t *testing.T) {
	trailer := frameBytes([]byte(`{"error":{"code":"resource_exhausted"}}`))
	trailer[0] = flagTrailer
	r := &turnRead{frames: &frameReader{r: bytes.NewReader(trailer)}, model: "m", close: func() error { return nil }}
	_, err := drain(t, r)
	if err == nil || statusOf("resource_exhausted") != 429 {
		t.Fatalf("err = %v", err)
	}
	if _, ok := err.(*statusError); !ok {
		t.Fatalf("err = %T, want *statusError", err)
	}
}

func TestTurnReaderComposerTakesTheAnswerFromItsThinking(t *testing.T) {
	body := serverFrames(
		update(updateThinking, "plan</think>the answer"),
		message(serverInteraction, number(updateEnd, 0)))
	r := &turnRead{frames: &frameReader{r: bytes.NewReader(body)}, model: "composer-1", composer: true, close: func() error { return nil }}
	out, err := drain(t, r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(out, `"content":"the answer"`) {
		t.Fatalf("composer answer = %s", out)
	}
}

func TestFoldTailKeepsTheLastTurnsAndSystem(t *testing.T) {
	body := []byte(`{"model":"m","messages":[
		{"role":"system","content":"s"},
		{"role":"user","content":"1"},{"role":"assistant","content":"2"},
		{"role":"user","content":"3"},{"role":"assistant","content":"4"},
		{"role":"user","content":"5"}]}`)
	got := tailBody(body)
	if !strings.Contains(string(got), `"system"`) || strings.Contains(string(got), `"content":"1"`) {
		t.Fatalf("tail = %s", got)
	}
	if !strings.Contains(string(got), `"content":"5"`) {
		t.Fatalf("the current turn is missing: %s", got)
	}
}

func TestStatusOfMapsTheConnectCodes(t *testing.T) {
	for code, want := range map[string]int{
		"unauthenticated": 401, "permission_denied": 403, "resource_exhausted": 429,
		"unavailable": 503, "invalid_argument": 400, "something_else": 500,
	} {
		if got := statusOf(code); got != want {
			t.Errorf("statusOf(%q) = %d, want %d", code, got, want)
		}
	}
}

func TestCursorSendsAFrameAndAnswersInChunks(t *testing.T) {
	body := serverFrames(update(updateText, "hi"), message(serverInteraction, number(updateEnd, 0)))
	var gotHeader http.Header
	var gotFrame []byte
	// The service answers before it has read the whole request: the run frame
	// arrives first and the acks follow on the open stream, so the handler reads
	// one frame and writes the turn.
	// The service is HTTP/2 only, and only h2 carries a request body and an answer
	// at once, which is how an acknowledgement reaches it mid-turn.
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Clone()
		w.Header().Set("Content-Type", "application/connect+proto")
		w.WriteHeader(http.StatusOK)
		head := make([]byte, frameHeader)
		if _, err := io.ReadFull(r.Body, head); err != nil {
			return
		}
		n := binary.BigEndian.Uint32(head[1:])
		gotFrame = make([]byte, n)
		if _, err := io.ReadFull(r.Body, gotFrame); err != nil {
			return
		}
		_, _ = w.Write(body)
		w.(http.Flusher).Flush()
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()

	c := &Cursor{Default: &executor.Default{Client: srv.Client()}, PeekIdle: 2 * time.Second}
	req := executor.Request{
		URL:  srv.URL + "/agent.v1.AgentService/Run",
		Body: []byte(`{"model":"claude-4.5-sonnet","messages":[{"role":"user","content":"hello"}]}`),
		Key:  "token-value",
		Account: func(name string) string {
			if name == "machineId" {
				return "machine-1"
			}
			return ""
		},
	}
	resp, err := c.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	out, err := drain(t, resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(out, `"content":"hi"`) {
		t.Fatalf("answer = %s", out)
	}
	for _, k := range []string{"x-cursor-checksum", "x-client-key", "x-session-id", "x-cursor-client-os", "x-ghost-mode"} {
		if gotHeader.Get(k) == "" {
			t.Errorf("%s is missing from the request", k)
		}
	}
	if gotHeader.Get("Authorization") != "Bearer token-value" {
		t.Errorf("authorization = %q", gotHeader.Get("Authorization"))
	}
	if len(gotFrame) == 0 || !bytes.Contains(gotFrame, []byte("hello")) {
		t.Fatalf("the run frame does not carry the turn: % x", gotFrame)
	}
}

func TestCursorRefusesAnAccountWithoutAMachineID(t *testing.T) {
	c := &Cursor{Default: &executor.Default{}}
	_, err := c.Execute(context.Background(), executor.Request{Key: "t", Body: []byte(`{}`)})
	if err != executor.ErrBadAccount {
		t.Fatalf("err = %v, want ErrBadAccount", err)
	}
}

func FuzzTurnReader(f *testing.F) {
	f.Add(serverFrames(update(updateText, "hi")))
	f.Fuzz(func(t *testing.T, raw []byte) {
		r := &turnRead{frames: &frameReader{r: bytes.NewReader(raw)}, model: "m",
			write: func([]byte) error { return nil }, close: func() error { return nil }}
		buf := make([]byte, 1<<10)
		for i := 0; i < 64; i++ {
			if _, err := r.Read(buf); err != nil {
				return
			}
		}
	})
}
