package cursor

import (
	"encoding/json"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

// fields reads every occurrence of a repeated field.
func fields(t *testing.T, body []byte, num protowire.Number) [][]byte {
	t.Helper()
	var out [][]byte
	for len(body) > 0 {
		n, kind, m := protowire.ConsumeTag(body)
		if m < 0 {
			t.Fatalf("field %d: the message is malformed", num)
		}
		body = body[m:]
		if n == num && kind == protowire.BytesType {
			v, m := protowire.ConsumeBytes(body)
			if m < 0 {
				t.Fatalf("field %d: the message is malformed", num)
			}
			out = append(out, v)
			body = body[m:]
			continue
		}
		m = protowire.ConsumeFieldValue(n, kind, body)
		if m < 0 {
			t.Fatalf("field %d: the message is malformed", num)
		}
		body = body[m:]
	}
	return out
}

// field reads one field of a message for a test.
func field(t *testing.T, body []byte, num protowire.Number) []byte {
	t.Helper()
	v, ok := firstField(body, num)
	if !ok {
		t.Fatalf("field %d is missing", num)
	}
	return v.bytes
}

func TestRunRequestCarriesTheTurnAsTheServiceReadsIt(t *testing.T) {
	body := []byte(`{
		"model": "claude-4.5-sonnet",
		"reasoning_effort": "ultra",
		"messages": [
			{"role": "system", "content": "be brief"},
			{"role": "user", "content": "first"},
			{"role": "assistant", "content": "one", "tool_calls": [{"id": "c1", "type": "function", "function": {"name": "read", "arguments": "{}"}}]},
			{"role": "tool", "tool_call_id": "c1", "content": "file"},
			{"role": "user", "content": "second"}
		],
		"tools": [{"type": "function", "function": {"name": "read", "description": "read a file", "parameters": {"type": "object"}}}]
	}`)
	frame, err := runRequest(body, "claude-4.5-sonnet", "msg-1")
	if err != nil {
		t.Fatalf("runRequest: %v", err)
	}
	payload := frame[frameHeader:]
	request := field(t, payload, clientRunRequest)

	// The state is empty: a fresh session.
	if state := field(t, request, runState); len(state) != 0 {
		t.Fatalf("state = %q, want empty", state)
	}
	action := field(t, field(t, request, runConversation), actionUser)
	user := field(t, action, actionUser)
	if text := readText(user, userText); text != "be brief\n\nsecond" {
		t.Fatalf("user text = %q", text)
	}
	if id := readText(user, userID); id != "msg-1" {
		t.Fatalf("message id = %q", id)
	}
	if mode, _ := readNumber(user, userMode); mode != 1 {
		t.Fatalf("mode = %d, want 1", mode)
	}
	// Every earlier turn is one entry at field one, its role inside it.
	history := field(t, action, actionHistory)
	entries := fields(t, history, actionUser)
	if len(entries) != 3 {
		t.Fatalf("history holds %d turns, want 3", len(entries))
	}
	if first := readText(field(t, field(t, entries[0], historyUser), historyContent), historyContent); first != "first" {
		t.Fatalf("opening turn = %q", first)
	}
	assistantText := readText(field(t, field(t, entries[1], historyAssistant), historyContent), historyContent)
	if assistantText != "one\n[tool_call id=c1 name=read args={}]" {
		t.Fatalf("assistant history = %q", assistantText)
	}
	toolText := readText(field(t, field(t, entries[2], historyUser), historyContent), historyContent)
	if toolText != "file\n[tool_result id=c1]" {
		t.Fatalf("tool history = %q", toolText)
	}

	details := field(t, request, runModelDetails)
	if readText(details, modelName) != "claude-4.5-sonnet" || readText(details, modelDetailAlt) != "claude-4.5-sonnet" {
		t.Fatalf("model details = %q", details)
	}
	asked := field(t, request, runRequested)
	param := field(t, asked, requestedParam)
	if readText(param, paramName) != "effort" || readText(param, paramValue) != "max" {
		t.Fatalf("effort = %q/%q, want effort/max", readText(param, paramName), readText(param, paramValue))
	}
	tool := field(t, field(t, request, runMCPTools), mcpToolEntry)
	if readText(tool, toolName) != "read" || readText(tool, toolWireName) != "read" {
		t.Fatalf("tool definition = %q", tool)
	}
	if readText(tool, toolProvider) != toolProviderName {
		t.Fatalf("tool provider = %q", readText(tool, toolProvider))
	}
	if schema := decodeValue(field(t, tool, toolSchema)); schema == nil {
		t.Fatal("the tool carries no schema")
	}
}

func TestRequestedNamesTheEffortReasoningForAGPTModel(t *testing.T) {
	param := field(t, requested("gpt-5.2", "high"), requestedParam)
	if got := readText(param, paramName); got != "reasoning" {
		t.Fatalf("parameter = %q, want reasoning", got)
	}
	if got := effortValue("none"); got != "" {
		t.Fatalf("effortValue(none) = %q, want empty", got)
	}
	if got := effortValue("minimal"); got != "low" {
		t.Fatalf("effortValue(minimal) = %q, want low", got)
	}
}

func TestValueRoundTripsJSON(t *testing.T) {
	cases := []string{`null`, `true`, `1.5`, `"text"`, `[1,"a"]`, `{"a":1,"b":[true]}`}
	for _, raw := range cases {
		var want any
		if err := json.Unmarshal([]byte(raw), &want); err != nil {
			t.Fatalf("case %q: %v", raw, err)
		}
		got := decodeValue(encodeValue(json.RawMessage(raw)))
		gotJSON, _ := json.Marshal(got)
		wantJSON, _ := json.Marshal(want)
		if string(gotJSON) != string(wantJSON) {
			t.Errorf("%s: round trip = %s, want %s", raw, gotJSON, wantJSON)
		}
	}
}

func FuzzValueDecode(f *testing.F) {
	f.Add(encodeValue(json.RawMessage(`{"a":1}`)))
	f.Add([]byte{0x08, 0x01})
	f.Fuzz(func(t *testing.T, raw []byte) {
		_ = decodeValue(raw)
	})
}
