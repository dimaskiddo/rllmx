package cursor

import (
	"encoding/json"
	"fmt"

	"google.golang.org/protobuf/encoding/protowire"
)

// statusError is the RPC status the service sent in its trailer, mapped to the
// HTTP status the pipeline's error rules understand.
type statusError struct{ code string }

func (e *statusError) Error() string { return "cursor: the service refused the turn" }

// statusOf maps a Connect code to an HTTP status. An unknown code is the
// service's own server error.
func statusOf(code string) int {
	switch code {
	case "invalid_argument", "out_of_range", "failed_precondition":
		return 400
	case "unauthenticated":
		return 401
	case "permission_denied":
		return 403
	case "not_found":
		return 404
	case "already_exists", "aborted":
		return 409
	case "resource_exhausted":
		return 429
	case "unimplemented":
		return 501
	case "unavailable":
		return 503
	case "deadline_exceeded":
		return 504
	}
	return 500
}

// clientExec writes an ExecClientMessage answering a request: its id, the exec
// id it named, and the result field the request's variant holds.
func clientExec(id uint64, ref string, result uint64, payload []byte) []byte {
	parts := join(number(execID, id), text(execRef, ref), message(protowire.Number(result), payload))
	return frameBytes(message(serverExec, parts))
}

// clientKV writes a KvClientMessage: its id, the answer's field, and the
// metadata the request carried.
func clientKV(id uint64, result uint64, payload, metadata []byte) []byte {
	parts := join(number(kvID, id), message(protowire.Number(result), payload))
	if len(metadata) > 0 {
		parts = append(parts, message(kvMetadata, metadata)...)
	}
	return frameBytes(message(serverKV, parts))
}

// otherField returns the first field of a message that is neither of the two
// named. It is how a request this router does not know is answered in the shape
// it asked.
func otherField(body []byte, skip1, skip2 protowire.Number) uint64 {
	for len(body) > 0 {
		num, kind, n := protowire.ConsumeTag(body)
		if n < 0 {
			return 0
		}
		body = body[n:]
		if num != skip1 && num != skip2 {
			return uint64(num)
		}
		m := protowire.ConsumeFieldValue(num, kind, body)
		if m < 0 {
			return 0
		}
		body = body[m:]
	}
	return 0
}

// firstText is the first non-empty of two strings.
func firstText(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// lastIndex finds the last occurrence of sub.
func lastIndex(s, sub string) int {
	for i := len(s) - len(sub); i >= 0; i-- {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// chatChunk is one Chat Completions chunk. The service answers in this shape
// once the executor hands it over, so a client's own format is translated from
// it like any other upstream.
type chatChunk struct {
	ID      string       `json:"id"`
	Object  string       `json:"object"`
	Created int64        `json:"created"`
	Model   string       `json:"model"`
	Choices []chatChoice `json:"choices"`
}

type chatChoice struct {
	Index        int       `json:"index"`
	Delta        chatDelta `json:"delta"`
	FinishReason *string   `json:"finish_reason"`
}

type chatDelta struct {
	Content          string          `json:"content,omitempty"`
	ReasoningContent string          `json:"reasoning_content,omitempty"`
	ToolCalls        []chatToolDelta `json:"tool_calls,omitempty"`
}

type chatToolDelta struct {
	Index    int          `json:"index"`
	ID       string       `json:"id,omitempty"`
	Type     string       `json:"type,omitempty"`
	Function chatFunction `json:"function"`
}

type chatFunction struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// encodeChunks writes the deltas as SSE frames, then the turn's closing frame
// when it has ended.
func encodeChunks(model string, pending []chunk, finish string) []byte {
	var out []byte
	for _, c := range pending {
		if frame, err := chunkFrame(model, c, "", false); err == nil {
			out = append(out, frame...)
		}
	}
	if finish == "" {
		return out
	}
	if frame, err := chunkFrame(model, chunk{}, finish, true); err == nil {
		out = append(out, frame...)
	}
	return append(out, "data: [DONE]\n\n"...)
}

// chunkFrame writes one chunk as an SSE event.
func chunkFrame(model string, c chunk, finish string, last bool) ([]byte, error) {
	d := chatDelta{Content: c.text, ReasoningContent: c.thinking}
	if c.call != nil {
		d.ToolCalls = []chatToolDelta{{Type: "function", ID: c.call.ID,
			Function: chatFunction{Name: c.call.Name, Arguments: c.call.Arguments}}}
	}
	choice := chatChoice{Delta: d}
	if last {
		reason := finish
		choice.FinishReason = &reason
	}
	body, err := json.Marshal(chatChunk{
		ID: "chatcmpl-" + newMessageID(), Object: "chat.completion.chunk",
		Model: model, Choices: []chatChoice{choice},
	})
	if err != nil {
		return nil, err
	}
	return []byte(fmt.Sprintf("data: %s\n\n", body)), nil
}
