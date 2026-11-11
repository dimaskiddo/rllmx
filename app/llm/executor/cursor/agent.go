package cursor

import (
	"encoding/json"
	"errors"
	"math"

	"google.golang.org/protobuf/encoding/protowire"
)

// Field numbers of the agent service's messages. They are the service's wire
// format, not this program's: the names follow the reference's decode of it.
const (
	// AgentClientMessage.run_request
	clientRunRequest = 1
	// AgentServerMessage: the answer's deltas, an exec request, a kv request.
	serverInteraction = 1
	serverExec        = 2
	serverKV          = 4
	// RunRequest: a fresh state, the conversation, the model, the tools, the
	// model ask.
	runState        = 1
	runConversation = 2
	runModelDetails = 3
	runMCPTools     = 4
	runRequested    = 9
	// ConversationAction.user_message_action / UserMessageAction.history
	actionUser    = 1
	actionHistory = 7
	// UserMessage: the text, its id, an empty context, the mode.
	userText = 1
	userID   = 2
	userCtx  = 3
	userMode = 4
	// A history entry: one role, one message whose field 1 repeats the text.
	historyUser      = 1
	historyAssistant = 2
	historyContent   = 1
	// ModelDetails and RequestedModel.
	modelName      = 1
	modelDetailAlt = 3
	modelDetailTwo = 4
	requestedFlag  = 7
	requestedParam = 3
	paramName      = 1
	paramValue     = 2
	// InteractionUpdate: a text delta, a thinking delta, the turn's end.
	updateText     = 1
	updateThinking = 4
	updateEnd      = 14
	// ExecServerMessage: its id, its exec id, a context request, an MCP call.
	execID          = 1
	execRef         = 15
	execContext     = 10
	execMCPCall     = 11
	execResultCtx   = 10
	mcpArgsName     = 1
	mcpArgsEntry    = 2
	mcpArgsCallID   = 3
	mcpArgsToolName = 5
	// KvServerMessage: its id, get, set, metadata; the client answers in kind.
	kvID       = 1
	kvGet      = 2
	kvSet      = 3
	kvMetadata = 4
	// McpToolDefinition.
	toolName        = 1
	toolDescription = 2
	toolSchema      = 3
	toolProvider    = 4
	toolWireName    = 5
	// google.protobuf.Value: the oneof body and a struct's fields.
	valueNull   = 1
	valueNumber = 2
	valueString = 3
	valueBool   = 4
	valueStruct = 5
	valueList   = 6
	structField = 1
	mapKey      = 1
	mapValue    = 2
	listValue   = 1
)

// errMessage means a payload is not the message it should be.
var errMessage = errors.New("cursor: the answer holds an unknown message")

// The wire primitives, named here so the decoders read as one thing.
var (
	consumeTag        = protowire.ConsumeTag
	consumeFieldValue = protowire.ConsumeFieldValue
	consumeBytes      = protowire.ConsumeBytes
)

// bytesType is the length-delimited wire type.
const bytesType = protowire.BytesType

// text is one length-delimited field holding a string.
func text(field protowire.Number, s string) []byte {
	return protowire.AppendString(protowire.AppendTag(nil, field, protowire.BytesType), s)
}

// message is one length-delimited field holding an encoded message.
func message(field protowire.Number, body []byte) []byte {
	return protowire.AppendBytes(protowire.AppendTag(nil, field, protowire.BytesType), body)
}

// number is one varint field.
func number(field protowire.Number, v uint64) []byte {
	return protowire.AppendVarint(protowire.AppendTag(nil, field, protowire.VarintType), v)
}

// join appends every part.
func join(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// readText returns the first string of a field, or "".
func readText(body []byte, field protowire.Number) string {
	v, ok := firstField(body, field)
	if !ok || v.kind != protowire.BytesType {
		return ""
	}
	return string(v.bytes)
}

// readMessage returns the first message of a field.
func readMessage(body []byte, field protowire.Number) ([]byte, bool) {
	v, ok := firstField(body, field)
	if !ok || v.kind != protowire.BytesType {
		return nil, false
	}
	return v.bytes, true
}

// readNumber returns the first varint of a field.
func readNumber(body []byte, field protowire.Number) (uint64, bool) {
	v, ok := firstField(body, field)
	if !ok || v.kind != protowire.VarintType {
		return 0, false
	}
	return v.num, true
}

// has reports a field.
func has(body []byte, field protowire.Number) bool {
	_, ok := firstField(body, field)
	return ok
}

// fieldValue is one decoded field.
type fieldValue struct {
	kind  protowire.Type
	num   uint64
	bytes []byte
}

// firstField finds a field, refusing a body that is not protobuf.
func firstField(body []byte, field protowire.Number) (fieldValue, bool) {
	for len(body) > 0 {
		num, kind, n := protowire.ConsumeTag(body)
		if n < 0 {
			return fieldValue{}, false
		}
		body = body[n:]
		var v fieldValue
		switch kind {
		case protowire.VarintType:
			v.num, n = protowire.ConsumeVarint(body)
		case protowire.BytesType:
			v.bytes, n = protowire.ConsumeBytes(body)
		case protowire.Fixed32Type:
			var f32 uint32
			f32, n = protowire.ConsumeFixed32(body)
			v.num = uint64(f32)
		case protowire.Fixed64Type:
			v.num, n = protowire.ConsumeFixed64(body)
		default:
			n = protowire.ConsumeFieldValue(num, kind, body)
		}
		if n < 0 {
			return fieldValue{}, false
		}
		body = body[n:]
		if num == field {
			v.kind = kind
			return v, true
		}
	}
	return fieldValue{}, false
}

// encodeValue writes a JSON value as a google.protobuf.Value: the oneof body
// only, because the field that carries it tags the message itself.
func encodeValue(raw json.RawMessage) []byte {
	var v any
	if len(raw) == 0 || json.Unmarshal(raw, &v) != nil {
		return number(valueNull, 0)
	}
	return valueOf(v)
}

// valueOf writes a decoded JSON value as google.protobuf.Value.
func valueOf(v any) []byte {
	switch x := v.(type) {
	case nil:
		return number(valueNull, 0)
	case bool:
		if x {
			return number(valueBool, 1)
		}
		return number(valueBool, 0)
	case float64:
		return protowire.AppendFixed64(protowire.AppendTag(nil, valueNumber, protowire.Fixed64Type), mathBits(x))
	case string:
		return text(valueString, x)
	case []any:
		var items []byte
		for _, item := range x {
			items = append(items, message(listValue, valueOf(item))...)
		}
		return message(valueList, items)
	case map[string]any:
		var fields []byte
		for k, val := range x {
			fields = append(fields, message(structField, join(text(mapKey, k), message(mapValue, valueOf(val))))...)
		}
		return message(valueStruct, fields)
	}
	return number(valueNull, 0)
}

// decodeValue reads a google.protobuf.Value back into a JSON value.
func decodeValue(body []byte) any {
	switch {
	case has(body, valueNull):
		return nil
	case has(body, valueBool):
		n, _ := readNumber(body, valueBool)
		return n != 0
	case has(body, valueNumber):
		if v, ok := firstField(body, valueNumber); ok {
			return floatFromBits(v.num)
		}
		return nil
	case has(body, valueString):
		return readText(body, valueString)
	case has(body, valueList):
		list, ok := readMessage(body, valueList)
		if !ok {
			return nil
		}
		return decodeList(list)
	case has(body, valueStruct):
		fields, ok := readMessage(body, valueStruct)
		if !ok {
			return nil
		}
		return decodeStruct(fields)
	}
	return nil
}

// decodeList reads a repeated value.
func decodeList(body []byte) []any {
	var out []any
	for len(body) > 0 {
		num, kind, n := protowire.ConsumeTag(body)
		if n < 0 {
			return out
		}
		body = body[n:]
		if num == listValue && kind == protowire.BytesType {
			v, m := protowire.ConsumeBytes(body)
			if m < 0 {
				return out
			}
			out = append(out, decodeValue(v))
			body = body[m:]
			continue
		}
		m := protowire.ConsumeFieldValue(num, kind, body)
		if m < 0 {
			return out
		}
		body = body[m:]
	}
	return out
}

// decodeStruct reads a map's entries.
func decodeStruct(body []byte) map[string]any {
	out := map[string]any{}
	for len(body) > 0 {
		num, kind, n := protowire.ConsumeTag(body)
		if n < 0 {
			return out
		}
		body = body[n:]
		if num != structField || kind != protowire.BytesType {
			m := protowire.ConsumeFieldValue(num, kind, body)
			if m < 0 {
				return out
			}
			body = body[m:]
			continue
		}
		entry, m := protowire.ConsumeBytes(body)
		if m < 0 {
			return out
		}
		body = body[m:]
		key := readText(entry, mapKey)
		if value, ok := readMessage(entry, mapValue); ok {
			out[key] = decodeValue(value)
		}
	}
	return out
}

// mathBits and floatFromBits move a JSON number through the fixed64 a protobuf
// double takes.
func mathBits(f float64) uint64 { return math.Float64bits(f) }

func floatFromBits(b uint64) float64 { return math.Float64frombits(b) }
