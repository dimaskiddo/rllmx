package translate

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/tidwall/gjson"
)

func EffortToThinking(e string) Thinking {
	switch strings.ToLower(e) {
	case "none", "off":
		return Thinking{Mode: "none"}
	case "auto":
		return Thinking{Mode: "auto"}
	}
	return Thinking{Mode: "level", Level: strings.ToLower(e)}
}

// ArgsText is the arguments of a call as JSON text: a string stays as it is, an
// object (some clients send one) is encoded.
func ArgsText(r gjson.Result) string {
	switch {
	case !r.Exists() || r.Type == gjson.Null:
		return ""
	case r.Type == gjson.String:
		return r.String()
	}
	return r.Raw
}

func ReadOpenAITool(t gjson.Result) Tool {
	if t.Get("type").String() == "function" || t.Get("function").Exists() {
		return Tool{
			Name: t.Get("function.name").String(), Description: t.Get("function.description").String(),
			Parameters: json.RawMessage(t.Get("function.parameters").Raw),
		}
	}
	return Tool{Raw: json.RawMessage(t.Raw)}
}

func ReadOpenAIToolChoice(c gjson.Result) *ToolChoice {
	switch {
	case !c.Exists() || c.Type == gjson.Null:
		return nil
	case c.Type == gjson.String:
		return &ToolChoice{Mode: c.String()}
	case c.Get("function.name").Exists():
		return &ToolChoice{Mode: "tool", Name: c.Get("function.name").String()}
	}
	return nil
}

// AppendJSONString appends s as a JSON string exa/ U+FFFD. A fuzz test holds it to json.Marshal.
func AppendJSONString(b []byte, s string) []byte {
	const hex = "0123456789abcdef"
	b = append(b, '"')
	start := 0
	for i := 0; i < len(s); {
		if c := s[i]; c < utf8.RuneSelf {
			if c >= 0x20 && c != '"' && c != '\\' && c != '<' && c != '>' && c != '&' {
				i++
				continue
			}
			b = append(b, s[start:i]...)
			switch c {
			case '\\', '"':
				b = append(b, '\\', c)
			case '\b':
				b = append(b, '\\', 'b')
			case '\f':
				b = append(b, '\\', 'f')
			case '\n':
				b = append(b, '\\', 'n')
			case '\r':
				b = append(b, '\\', 'r')
			case '\t':
				b = append(b, '\\', 't')
			default:
				b = append(b, '\\', 'u', '0', '0', hex[c>>4], hex[c&0xF])
			}
			i++
			start = i
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b = append(b, s[start:i]...)
			b = append(b, '\\', 'u', 'f', 'f', 'f', 'd')
			i += size
			start = i
			continue
		}
		if r == ' ' || r == ' ' {
			b = append(b, s[start:i]...)
			b = append(b, `\u202`...)
			b = append(b, hex[r&0xF])
			i += size
			start = i
			continue
		}
		i += size
	}
	b = append(b, s[start:]...)
	return append(b, '"')
}

// ReadErrorObject reads an error a stream carried: either a bare message or an
// object with a type and a message.
func ReadErrorObject(e gjson.Result) *ChunkError {
	if e.Type == gjson.String {
		return &ChunkError{Message: e.String()}
	}
	return &ChunkError{Type: e.Get("type").String(), Message: e.Get("message").String()}
}

// MustJSON encodes a value as JSON text, or "" when it cannot.
func MustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func RawOrEmpty(args string) json.RawMessage {
	if args == "" || !json.Valid([]byte(args)) {
		return json.RawMessage("{}")
	}
	return json.RawMessage(args)
}

func FirstRaw(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
