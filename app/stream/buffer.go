package stream

import (
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"rllmx/app/translate"
)

// ClientBuffer is the count added to every prompt a client is told about.
const ClientBuffer = 2000

// AddClientBuffer adds ClientBuffer tokens to the prompt count of a usage
// object in a client-bound payload, and to the total beside it where the format
// has one. A prompt count of 0 stays 0. It is the last step before a write and
// never reaches the stored row. A payload with no usage object, or one that is
// not JSON, comes back as it was.
func AddClientBuffer(format string, payload []byte) []byte {
	if !gjson.ValidBytes(payload) {
		return payload
	}
	for _, p := range bufferPaths(format, payload) {
		payload = bump(payload, p)
	}
	return payload
}

type usagePath struct{ obj, prompt, total string }

// bufferPaths lists the usage objects a payload of format may carry.
func bufferPaths(format string, payload []byte) []usagePath {
	switch format {
	case translate.FormatClaude:
		return []usagePath{{"message.usage", "input_tokens", ""}, {"usage", "input_tokens", ""}}
	case translate.FormatResponses:
		return []usagePath{{"response.usage", "input_tokens", "total_tokens"}, {"usage", "input_tokens", "total_tokens"}}
	case translate.FormatGemini:
		return []usagePath{{"usageMetadata", "promptTokenCount", "totalTokenCount"}, {"response.usageMetadata", "promptTokenCount", "totalTokenCount"}}
	case translate.FormatOllama:
		if gjson.GetBytes(payload, "done").Bool() {
			return []usagePath{{"", "prompt_eval_count", ""}}
		}
		return nil
	}
	return []usagePath{{"usage", "prompt_tokens", "total_tokens"}}
}

func join(obj, key string) string {
	if obj == "" {
		return key
	}
	return obj + "." + key
}

func bump(b []byte, p usagePath) []byte {
	prompt := gjson.GetBytes(b, join(p.obj, p.prompt))
	if !prompt.Exists() || prompt.Int() <= 0 {
		return b
	}
	out, err := sjson.SetBytes(b, join(p.obj, p.prompt), prompt.Int()+ClientBuffer)
	if err != nil {
		return b
	}
	if p.total != "" {
		if total := gjson.GetBytes(out, join(p.obj, p.total)); total.Exists() {
			if o, err := sjson.SetBytes(out, join(p.obj, p.total), total.Int()+ClientBuffer); err == nil {
				out = o
			}
		}
	}
	return out
}
