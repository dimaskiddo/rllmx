package cursor

import "github.com/tidwall/sjson"

// Fields of the service's usable-model answer: the models it will run, at field
// one, and the picker's own flags at field two.
const (
	modelsList          = 1
	modelsName          = 1
	modelsPicker        = 2
	pickerName          = 1
	pickerDefaultOn     = 2
	pickerChatOnly      = 4
	pickerSupportsAgent = 5
	pickerHidden        = 35
)

// usableModels reads the model ids the account may use, in the order the
// service sends them.
func usableModels(raw []byte) []string {
	var out []string
	seen := map[string]bool{}
	for len(raw) > 0 {
		num, kind, n := consumeTag(raw)
		if n < 0 {
			return out
		}
		raw = raw[n:]
		if num != modelsList || kind != bytesType {
			m := consumeFieldValue(num, kind, raw)
			if m < 0 {
				return out
			}
			raw = raw[m:]
			continue
		}
		entry, m := consumeBytes(raw)
		if m < 0 {
			return out
		}
		raw = raw[m:]
		id := readText(entry, modelsName)
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// autoModel picks what the service's own "Auto" entry stands for: a model the
// picker marks as its default, else the first it marks agent-capable, else the
// first it lists. A model flagged chat-only or hidden is never picked.
func autoModelFrom(raw []byte) string {
	type pick struct {
		name   string
		agent  bool
		def    bool
		chat   bool
		hidden bool
	}
	var picks []pick
	for len(raw) > 0 {
		num, kind, n := consumeTag(raw)
		if n < 0 {
			break
		}
		raw = raw[n:]
		if num != modelsPicker || kind != bytesType {
			m := consumeFieldValue(num, kind, raw)
			if m < 0 {
				break
			}
			raw = raw[m:]
			continue
		}
		entry, m := consumeBytes(raw)
		if m < 0 {
			break
		}
		raw = raw[m:]
		name := readText(entry, pickerName)
		if name == "" {
			continue
		}
		on, _ := readNumber(entry, pickerDefaultOn)
		agent, _ := readNumber(entry, pickerSupportsAgent)
		chat, _ := readNumber(entry, pickerChatOnly)
		hidden, _ := readNumber(entry, pickerHidden)
		picks = append(picks, pick{name: name, agent: agent != 0, def: on != 0, chat: chat != 0, hidden: hidden != 0})
	}
	for _, p := range picks {
		if p.def && p.agent && !p.chat && !p.hidden {
			return p.name
		}
	}
	for _, p := range picks {
		if p.agent && !p.chat && !p.hidden {
			return p.name
		}
	}
	for _, p := range picks {
		if !p.chat && !p.hidden {
			return p.name
		}
	}
	return ""
}

// rawSet replaces one top-level field and keeps the body when the write fails.
func rawSet(body []byte, path string, v any) []byte {
	if next, err := sjson.SetBytes(body, path, v); err == nil {
		return next
	}
	return body
}
