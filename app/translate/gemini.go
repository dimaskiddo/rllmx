package translate

import (
	"cmp"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
)

// GeminiCodec joins Gemini generateContent to the hub.
type GeminiCodec struct{}

func init() { Register(GeminiCodec{}) }

// Name is the format name.
func (GeminiCodec) Name() string { return FormatGemini }

var geminiKnown = set("contents", "systemInstruction", "system_instruction", "tools", "toolConfig", "tool_config",
	"generationConfig", "generation_config", "safetySettings", "model")

// ReadRequest reads a generateContent body. The model comes from the URL, so
// the caller sets Request.Model.
func (GeminiCodec) ReadRequest(body []byte, c *Ctx) (*Request, error) {
	if !validObject(body) {
		return nil, ErrBadBody
	}
	g := gjson.ParseBytes(body)
	req := &Request{Model: g.Get("model").String()}

	sys := g.Get("systemInstruction")
	if !sys.Exists() {
		sys = g.Get("system_instruction")
	}
	if t := geminiPartsText(sys.Get("parts")); t != "" {
		req.Messages = append(req.Messages, Message{Role: RoleSystem, Content: TextPart(t)})
	}
	contents := g.Get("contents")
	if contents.Exists() && !contents.IsArray() {
		return nil, ErrBadBody
	}

	// A result without an id answers the oldest unanswered call of its name.
	type open struct{ id, name string }
	var unanswered []open
	next := 0
	newID := func(name string) string {
		next++
		return fmt.Sprintf("call_%d_%s", next, cleanName(name))
	}

	for _, ct := range contents.Array() {
		if !ct.IsObject() {
			return nil, ErrBadBody
		}
		role := RoleUser
		if ct.Get("role").String() == "model" {
			role = RoleAssistant
		}
		msg := Message{Role: role}
		var results []Message
		for _, p := range ct.Get("parts").Array() {
			switch {
			case p.Get("thought").Bool() && p.Get("text").Exists():
				msg.Reasoning += p.Get("text").String()
			case p.Get("text").Exists():
				msg.Content = append(msg.Content, Part{Type: PartText, Text: p.Get("text").String()})
			case p.Get("inlineData").Exists() || p.Get("inline_data").Exists():
				d := p.Get("inlineData")
				if !d.Exists() {
					d = p.Get("inline_data")
				}
				mt := cmp.Or(d.Get("mimeType").String(), d.Get("mime_type").String())
				data := d.Get("data").String()
				switch {
				case strings.HasPrefix(mt, "image/"):
					msg.Content = append(msg.Content, Part{Type: PartImage, MediaType: mt, Data: data})
				case strings.HasPrefix(mt, "audio/"):
					msg.Content = append(msg.Content, Part{Type: PartAudio, MediaType: mt, Data: data})
				default:
					msg.Content = append(msg.Content, Part{Type: PartFile, MediaType: mt, Data: data})
				}
			case p.Get("functionCall").Exists():
				fc := p.Get("functionCall")
				id := fc.Get("id").String()
				if id == "" {
					id = newID(fc.Get("name").String())
				}
				args := fc.Get("args").Raw
				if args == "" {
					args = "{}"
				}
				tc := ToolCall{ID: id, Name: fc.Get("name").String(), Arguments: args, Signature: cmp.Or(p.Get("thoughtSignature").String(), p.Get("thought_signature").String())}
				msg.ToolCalls = append(msg.ToolCalls, tc)
				unanswered = append(unanswered, open{id: id, name: tc.Name})
			case p.Get("functionResponse").Exists():
				fr := p.Get("functionResponse")
				id := fr.Get("id").String()
				name := fr.Get("name").String()
				if id == "" {
					for i, o := range unanswered {
						if o.name == name {
							id = o.id
							unanswered = append(unanswered[:i], unanswered[i+1:]...)
							break
						}
					}
				}
				if id == "" {
					id = newID(name)
				}
				resp := fr.Get("response")
				text := resp.Raw
				if r := resp.Get("result"); r.Exists() && r.Type == gjson.String {
					text = r.String()
				}
				results = append(results, Message{Role: RoleTool, ToolCallID: id, Name: name, Content: TextPart(text)})
			}
		}
		if len(results) > 0 {
			req.Messages = append(req.Messages, results...)
			if len(msg.Content) > 0 {
				req.Messages = append(req.Messages, msg)
			}
			continue
		}
		if len(msg.Content) > 0 || len(msg.ToolCalls) > 0 || msg.Reasoning != "" {
			req.Messages = append(req.Messages, msg)
		}
	}
	// A system-only request is a user turn: Gemini refuses empty contents.
	if len(req.Messages) == 1 && req.Messages[0].Role == RoleSystem {
		req.Messages[0].Role = RoleUser
	}

	for _, t := range g.Get("tools").Array() {
		for _, fd := range t.Get("functionDeclarations").Array() {
			req.Tools = append(req.Tools, Tool{Name: fd.Get("name").String(), Description: fd.Get("description").String(), Parameters: json.RawMessage(firstRaw(fd.Get("parameters").Raw, fd.Get("parametersJsonSchema").Raw))})
		}
	}
	tc := g.Get("toolConfig.functionCallingConfig")
	if !tc.Exists() {
		tc = g.Get("tool_config.function_calling_config")
	}
	switch tc.Get("mode").String() {
	case "AUTO":
		req.ToolChoice = &ToolChoice{Mode: "auto"}
	case "NONE":
		req.ToolChoice = &ToolChoice{Mode: "none"}
	case "ANY":
		names := tc.Get("allowedFunctionNames").Array()
		if len(names) == 1 {
			req.ToolChoice = &ToolChoice{Mode: "tool", Name: names[0].String()}
		} else {
			req.ToolChoice = &ToolChoice{Mode: "required"}
		}
	}

	gc := g.Get("generationConfig")
	if !gc.Exists() {
		gc = g.Get("generation_config")
	}
	p := &req.Params
	p.MaxTokens = intPtr(gc.Get("maxOutputTokens"))
	p.Temperature = floatPtr(gc.Get("temperature"))
	p.TopP = floatPtr(gc.Get("topP"))
	p.TopK = intPtr(gc.Get("topK"))
	p.Seed = int64Ptr(gc.Get("seed"))
	p.Stop = strList(gc.Get("stopSequences"))
	p.FrequencyPenalty = floatPtr(gc.Get("frequencyPenalty"))
	p.PresencePenalty = floatPtr(gc.Get("presencePenalty"))
	if gc.Get("responseMimeType").String() == "application/json" {
		if schema := gc.Get("responseSchema"); schema.Exists() {
			req.Params.ResponseFormat = json.RawMessage(mustJSON(obj{"type": "json_schema", "json_schema": obj{"name": "response", "schema": json.RawMessage(schema.Raw)}}))
		} else {
			req.Params.ResponseFormat = json.RawMessage(`{"type":"json_object"}`)
		}
	}
	if th := gc.Get("thinkingConfig"); th.Exists() {
		switch {
		case th.Get("thinkingBudget").Exists():
			b := int(th.Get("thinkingBudget").Int())
			if b == 0 {
				req.Thinking = Thinking{Mode: "none"}
			} else if b < 0 {
				req.Thinking = Thinking{Mode: "auto"}
			} else {
				req.Thinking = Thinking{Mode: "budget", Budget: b}
			}
		case th.Get("thinkingLevel").Exists():
			req.Thinking = Thinking{Mode: "level", Level: strings.ToLower(th.Get("thinkingLevel").String())}
		}
	}
	req.Extra = extraOf(body, geminiKnown)
	return req, nil
}

func firstRaw(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func geminiPartsText(parts gjson.Result) string {
	var texts []string
	for _, p := range parts.Array() {
		if t := p.Get("text").String(); t != "" {
			texts = append(texts, t)
		}
	}
	return strings.Join(texts, "\n")
}

// geminiTurn is one entry of the contents array.
type geminiTurn struct {
	Role  string `json:"role"`
	Parts []any  `json:"parts"`
}

// WriteRequest writes the hub as a generateContent body. The model is not in
// the body; the URL carries it.
func (GeminiCodec) WriteRequest(r *Request, c *Ctx) ([]byte, error) {
	var system []string
	var turns []geminiTurn
	names := map[string]string{}
	for _, m := range r.Messages {
		for _, tc := range m.ToolCalls {
			names[tc.ID] = tc.Name
		}
	}

	// Every call id is unique over the whole history; a result takes the id its
	// call was given. Results are queued per id because one id can answer two
	// different calls in a long session.
	used := map[string]bool{}
	queues := map[string][]string{}
	uniq := func(id string) string {
		if id == "" || !used[id] {
			used[id] = true
			return id
		}
		for n := 2; ; n++ {
			cand := fmt.Sprintf("%s-%d", id, n)
			if !used[cand] {
				used[cand] = true
				return cand
			}
		}
	}

	add := func(role string, parts []any) {
		if len(parts) == 0 {
			return
		}
		if n := len(turns); n > 0 && turns[n-1].Role == role {
			turns[n-1].Parts = append(turns[n-1].Parts, parts...)
			return
		}
		turns = append(turns, geminiTurn{Role: role, Parts: parts})
	}

	for _, m := range r.Messages {
		switch m.Role {
		case RoleSystem:
			if t := m.Text(); strings.TrimSpace(t) != "" {
				system = append(system, t)
			}
		case RoleTool:
			id := m.ToolCallID
			if q := queues[id]; len(q) > 0 {
				id, queues[id] = q[0], q[1:]
			}
			name := cmp.Or(m.Name, names[m.ToolCallID], "tool")
			add("user", []any{obj{"functionResponse": obj{"id": id, "name": sanitizeGeminiName(name), "response": obj{"result": m.Text()}}}})
		case RoleAssistant:
			var parts []any
			if m.Reasoning != "" {
				parts = append(parts, obj{"text": m.Reasoning, "thought": true})
			}
			for _, p := range m.Content {
				if p.Type == PartText && strings.TrimSpace(p.Text) != "" {
					parts = append(parts, obj{"text": p.Text})
				}
			}
			first := true
			for _, tc := range m.ToolCalls {
				id := uniq(tc.ID)
				queues[tc.ID] = append(queues[tc.ID], id)
				fc := obj{"id": id, "name": sanitizeGeminiName(tc.Name), "args": rawOrEmpty(tc.Arguments)}
				part := obj{"functionCall": fc}
				sig := c.signatureFor(tc)
				if sig == "" && first {
					sig = c.Signature
				}
				first = false
				if sig != "" {
					part["thoughtSignature"] = sig
				}
				parts = append(parts, part)
			}
			add("model", parts)
		default:
			add("user", geminiContent(m.Content))
		}
	}
	// Gemini starts and ends on a user turn.
	if len(turns) > 0 && turns[0].Role != "user" {
		turns = append([]geminiTurn{{Role: "user", Parts: []any{obj{"text": "..."}}}}, turns...)
	}
	if n := len(turns); n > 0 && turns[n-1].Role != "user" {
		turns = append(turns, geminiTurn{Role: "user", Parts: []any{obj{"text": "Continue."}}})
	}
	// A history that ends on a function call is answered with a function response.
	if len(turns) == 0 {
		turns = []geminiTurn{{Role: "user", Parts: []any{obj{"text": "..."}}}}
	}

	o := obj{"contents": turns}
	if len(system) > 0 {
		o["systemInstruction"] = obj{"role": "user", "parts": []any{obj{"text": strings.Join(system, "\n")}}}
	}
	if len(r.Tools) > 0 {
		var decls []any
		for _, t := range r.Tools {
			if len(t.Raw) > 0 {
				continue
			}
			decls = append(decls, obj{"name": sanitizeGeminiName(t.Name), "description": t.Description, "parameters": cleanGeminiSchema(t.Parameters)})
		}
		if len(decls) > 0 {
			o["tools"] = []any{obj{"functionDeclarations": decls}}
		}
	}
	if tc := r.ToolChoice; tc != nil {
		cfg := obj{}
		switch tc.Mode {
		case "auto":
			cfg["mode"] = "AUTO"
		case "none":
			cfg["mode"] = "NONE"
		case "required":
			cfg["mode"] = "ANY"
		case "tool":
			cfg["mode"] = "ANY"
			cfg["allowedFunctionNames"] = []string{sanitizeGeminiName(tc.Name)}
		}
		o["toolConfig"] = obj{"functionCallingConfig": cfg}
	}
	if gc := geminiGenerationConfig(r, c); len(gc) > 0 {
		o["generationConfig"] = gc
	}
	if c.Safety {
		o["safetySettings"] = safetyOff
	}
	return json.Marshal(withExtra(o, r.Extra))
}

// safetyOff turns every harm filter off, the setting Gemini CLI sends.
var safetyOff = []any{
	obj{"category": "HARM_CATEGORY_HATE_SPEECH", "threshold": "OFF"},
	obj{"category": "HARM_CATEGORY_DANGEROUS_CONTENT", "threshold": "OFF"},
	obj{"category": "HARM_CATEGORY_SEXUALLY_EXPLICIT", "threshold": "OFF"},
	obj{"category": "HARM_CATEGORY_HARASSMENT", "threshold": "OFF"},
	obj{"category": "HARM_CATEGORY_CIVIC_INTEGRITY", "threshold": "OFF"},
}

func rawOrEmpty(args string) json.RawMessage {
	if args == "" || !json.Valid([]byte(args)) {
		return json.RawMessage("{}")
	}
	return json.RawMessage(args)
}

// signatureFor picks the thought signature a call goes out with: the one the
// client returned, else one remembered from the answer that created it.
func (c *Ctx) signatureFor(tc ToolCall) string {
	if tc.Signature != "" {
		return tc.Signature
	}
	if c.Calls != nil {
		if s, ok := c.Calls.Get(tc.ID); ok {
			return s
		}
	}
	return ""
}

func geminiContent(parts []Part) []any {
	var out []any
	for _, p := range parts {
		switch p.Type {
		case PartText:
			if strings.TrimSpace(p.Text) != "" {
				out = append(out, obj{"text": p.Text})
			}
		case PartImage, PartAudio, PartFile:
			data, mt := p.Data, p.MediaType
			if data == "" {
				if rest, ok := strings.CutPrefix(p.URL, "data:"); ok {
					meta, d, found := strings.Cut(rest, ",")
					if found && strings.HasSuffix(meta, ";base64") {
						data, mt = d, strings.TrimSuffix(meta, ";base64")
					}
				}
			}
			if data != "" {
				out = append(out, obj{"inlineData": obj{"mimeType": mt, "data": data}})
			} else if p.URL != "" {
				out = append(out, obj{"fileData": obj{"mimeType": mt, "fileUri": p.URL}})
			}
		}
	}
	return out
}

func geminiGenerationConfig(r *Request, c *Ctx) obj {
	gc := obj{}
	p := r.Params
	// Gemini 4 removed the sampling parameters; sending them fails the request.
	sampling := !strings.Contains(strings.ToLower(r.Model), "gemini-4")
	if sampling {
		if p.Temperature != nil {
			gc["temperature"] = *p.Temperature
		}
		if p.TopP != nil {
			gc["topP"] = *p.TopP
		}
		if p.TopK != nil {
			gc["topK"] = *p.TopK
		}
	}
	if p.MaxTokens != nil {
		gc["maxOutputTokens"] = *p.MaxTokens
	}
	if p.Seed != nil {
		gc["seed"] = *p.Seed
	}
	if len(p.Stop) > 0 {
		gc["stopSequences"] = p.Stop
	}
	if p.FrequencyPenalty != nil {
		gc["frequencyPenalty"] = *p.FrequencyPenalty
	}
	if p.PresencePenalty != nil {
		gc["presencePenalty"] = *p.PresencePenalty
	}
	if len(p.ResponseFormat) > 0 {
		var rf struct {
			Type       string `json:"type"`
			JSONSchema struct {
				Schema json.RawMessage `json:"schema"`
			} `json:"json_schema"`
		}
		if json.Unmarshal(p.ResponseFormat, &rf) == nil && (rf.Type == "json_object" || rf.Type == "json_schema") {
			gc["responseMimeType"] = "application/json"
			if len(rf.JSONSchema.Schema) > 0 {
				gc["responseSchema"] = cleanGeminiSchema(rf.JSONSchema.Schema)
			}
		}
	}
	if th := r.Thinking; th.Set() {
		cfg := obj{"includeThoughts": true}
		switch th.Mode {
		case "none":
			cfg = obj{"thinkingBudget": 0}
		case "auto":
			cfg["thinkingBudget"] = -1
		case "budget":
			cfg["thinkingBudget"] = th.Budget
		case "level":
			if c.Caps.ThinkingFormat == "gemini-level" {
				cfg["thinkingLevel"] = strings.ToUpper(th.Level)
			} else {
				cfg["thinkingBudget"] = c.Budget(th.Level)
			}
		}
		gc["thinkingConfig"] = cfg
	}
	return gc
}

// ReadResponse reads a generateContent answer.
func (GeminiCodec) ReadResponse(body []byte, c *Ctx) (*Response, error) {
	if !validObject(body) {
		return nil, ErrBadBody
	}
	g := gjson.ParseBytes(body)
	resp := &Response{ID: g.Get("responseId").String(), Model: g.Get("modelVersion").String()}
	msg := Message{Role: RoleAssistant}
	cand := g.Get("candidates.0")
	for _, p := range cand.Get("content.parts").Array() {
		switch {
		case p.Get("functionCall").Exists():
			fc := p.Get("functionCall")
			id := fc.Get("id").String()
			if id == "" {
				id = fmt.Sprintf("call_%d_%s", len(msg.ToolCalls)+1, cleanName(fc.Get("name").String()))
			}
			args := fc.Get("args").Raw
			if args == "" {
				args = "{}"
			}
			tc := ToolCall{ID: id, Name: fc.Get("name").String(), Arguments: args, Signature: p.Get("thoughtSignature").String()}
			if tc.Signature != "" && c.Calls != nil {
				c.Calls.Put(id, tc.Signature)
			}
			msg.ToolCalls = append(msg.ToolCalls, tc)
		case p.Get("thought").Bool():
			msg.Reasoning += p.Get("text").String()
		case p.Get("text").Exists():
			msg.Content = append(msg.Content, Part{Type: PartText, Text: p.Get("text").String()})
		}
	}
	resp.Message = msg
	resp.FinishReason = FinishWithCalls(FinishFromGemini(cand.Get("finishReason").String()), len(msg.ToolCalls))
	resp.Usage = readGeminiUsage(g.Get("usageMetadata"))
	resp.Extra = extraOf(body, set("candidates", "usageMetadata", "modelVersion", "responseId"))
	return resp, nil
}

// readGeminiUsage reads Gemini's usage, where the prompt already includes the
// cached tokens and the completion is the candidates plus the thought tokens.
func readGeminiUsage(u gjson.Result) Usage {
	if !u.Exists() {
		return Usage{}
	}
	thoughts := u.Get("thoughtsTokenCount").Int()
	cand := u.Get("candidatesTokenCount").Int()
	if !u.Get("candidatesTokenCount").Exists() && u.Get("totalTokenCount").Exists() {
		cand = max(u.Get("totalTokenCount").Int()-u.Get("promptTokenCount").Int()-thoughts, 0)
	}
	return NormalizeUsage(Usage{
		Prompt: u.Get("promptTokenCount").Int(), Completion: cand + thoughts,
		Cached: u.Get("cachedContentTokenCount").Int(), Reasoning: thoughts,
	})
}

// WriteResponse writes the hub answer as a generateContent body.
func (GeminiCodec) WriteResponse(r *Response, c *Ctx) ([]byte, error) {
	var parts []any
	if r.Message.Reasoning != "" {
		parts = append(parts, obj{"text": r.Message.Reasoning, "thought": true})
	}
	for _, p := range r.Message.Content {
		if p.Type == PartText {
			parts = append(parts, obj{"text": p.Text})
		}
	}
	for _, tc := range r.Message.ToolCalls {
		part := obj{"functionCall": obj{"id": tc.ID, "name": tc.Name, "args": rawOrEmpty(tc.Arguments)}}
		if sig := c.signatureFor(tc); sig != "" {
			part["thoughtSignature"] = sig
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		parts = []any{obj{"text": ""}}
	}
	u := obj{"promptTokenCount": r.Usage.Prompt, "candidatesTokenCount": r.Usage.Completion - r.Usage.Reasoning, "totalTokenCount": r.Usage.Prompt + r.Usage.Completion}
	if r.Usage.Reasoning > 0 {
		u["thoughtsTokenCount"] = r.Usage.Reasoning
	}
	if r.Usage.Cached > 0 {
		u["cachedContentTokenCount"] = r.Usage.Cached
	}
	o := obj{
		"candidates":    []any{obj{"content": obj{"role": "model", "parts": parts}, "finishReason": FinishToGemini(r.FinishReason), "index": 0}},
		"usageMetadata": u, "modelVersion": r.Model, "responseId": r.ID,
	}
	return json.Marshal(withExtra(o, r.Extra))
}
