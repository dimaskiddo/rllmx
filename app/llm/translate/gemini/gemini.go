package gemini

import (
	"cmp"
	"encoding/json"
	"fmt"
	"rllmx/app/llm/translate"
	"slices"
	"strings"

	"github.com/tidwall/gjson"
)

// GeminiCodec joins Gemini generateContent to the hub.
type GeminiCodec struct{}

func init() { translate.Register(GeminiCodec{}) }

// Name is the format name.
func (GeminiCodec) Name() string { return translate.FormatGemini }

var geminiKnown = translate.Set("contents", "systemInstruction", "system_instruction", "tools", "toolConfig", "tool_config",
	"generationConfig", "generation_config", "safetySettings", "model")

// ReadRequest reads a generateContent body. The model comes from the URL, so
// the caller sets translate.Request.Model.
func (GeminiCodec) ReadRequest(body []byte, c *translate.Ctx) (*translate.Request, error) {
	if !translate.ValidObject(body) {
		return nil, translate.ErrBadBody
	}
	g := gjson.ParseBytes(body)
	req := &translate.Request{Model: g.Get("model").String()}

	sys := g.Get("systemInstruction")
	if !sys.Exists() {
		sys = g.Get("system_instruction")
	}
	if t := geminiPartsText(sys.Get("parts")); t != "" {
		req.Messages = append(req.Messages, translate.Message{Role: translate.RoleSystem, Content: translate.TextPart(t)})
	}
	contents := g.Get("contents")
	if contents.Exists() && !contents.IsArray() {
		return nil, translate.ErrBadBody
	}

	// A result without an id answers the oldest unanswered call of its name.
	type open struct{ id, name string }
	var unanswered []open
	next := 0
	newID := func(name string) string {
		next++
		return fmt.Sprintf("call_%d_%s", next, translate.CleanName(name))
	}

	for _, ct := range contents.Array() {
		if !ct.IsObject() {
			return nil, translate.ErrBadBody
		}
		role := translate.RoleUser
		if ct.Get("role").String() == "model" {
			role = translate.RoleAssistant
		}
		msg := translate.Message{Role: role}
		var results []translate.Message
		for _, p := range ct.Get("parts").Array() {
			switch {
			case p.Get("thought").Bool() && p.Get("text").Exists():
				msg.Reasoning += p.Get("text").String()
			case p.Get("text").Exists():
				msg.Content = append(msg.Content, translate.Part{Type: translate.PartText, Text: p.Get("text").String()})
			case p.Get("inlineData").Exists() || p.Get("inline_data").Exists():
				d := p.Get("inlineData")
				if !d.Exists() {
					d = p.Get("inline_data")
				}
				mt := cmp.Or(d.Get("mimeType").String(), d.Get("mime_type").String())
				data := d.Get("data").String()
				switch {
				case strings.HasPrefix(mt, "image/"):
					msg.Content = append(msg.Content, translate.Part{Type: translate.PartImage, MediaType: mt, Data: data})
				case strings.HasPrefix(mt, "audio/"):
					msg.Content = append(msg.Content, translate.Part{Type: translate.PartAudio, MediaType: mt, Data: data})
				default:
					msg.Content = append(msg.Content, translate.Part{Type: translate.PartFile, MediaType: mt, Data: data})
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
				tc := translate.ToolCall{ID: id, Name: fc.Get("name").String(), Arguments: args, Signature: cmp.Or(p.Get("thoughtSignature").String(), p.Get("thought_signature").String())}
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
				results = append(results, translate.Message{Role: translate.RoleTool, ToolCallID: id, Name: name, Content: translate.TextPart(text)})
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
	if len(req.Messages) == 1 && req.Messages[0].Role == translate.RoleSystem {
		req.Messages[0].Role = translate.RoleUser
	}

	for _, t := range g.Get("tools").Array() {
		for _, fd := range t.Get("functionDeclarations").Array() {
			req.Tools = append(req.Tools, translate.Tool{Name: fd.Get("name").String(), Description: fd.Get("description").String(), Parameters: json.RawMessage(translate.FirstRaw(fd.Get("parameters").Raw, fd.Get("parametersJsonSchema").Raw))})
		}
	}
	tc := g.Get("toolConfig.functionCallingConfig")
	if !tc.Exists() {
		tc = g.Get("tool_config.function_calling_config")
	}
	switch tc.Get("mode").String() {
	case "AUTO":
		req.ToolChoice = &translate.ToolChoice{Mode: "auto"}
	case "NONE":
		req.ToolChoice = &translate.ToolChoice{Mode: "none"}
	case "ANY":
		names := tc.Get("allowedFunctionNames").Array()
		if len(names) == 1 {
			req.ToolChoice = &translate.ToolChoice{Mode: "tool", Name: names[0].String()}
		} else {
			req.ToolChoice = &translate.ToolChoice{Mode: "required"}
		}
	}

	gc := g.Get("generationConfig")
	if !gc.Exists() {
		gc = g.Get("generation_config")
	}
	p := &req.Params
	p.MaxTokens = translate.IntPtr(gc.Get("maxOutputTokens"))
	p.Temperature = translate.FloatPtr(gc.Get("temperature"))
	p.TopP = translate.FloatPtr(gc.Get("topP"))
	p.TopK = translate.IntPtr(gc.Get("topK"))
	p.Seed = translate.Int64Ptr(gc.Get("seed"))
	p.Stop = translate.StrList(gc.Get("stopSequences"))
	p.FrequencyPenalty = translate.FloatPtr(gc.Get("frequencyPenalty"))
	p.PresencePenalty = translate.FloatPtr(gc.Get("presencePenalty"))
	if gc.Get("responseMimeType").String() == "application/json" {
		if schema := gc.Get("responseSchema"); schema.Exists() {
			req.Params.ResponseFormat = json.RawMessage(translate.MustJSON(translate.Obj{"type": "json_schema", "json_schema": translate.Obj{"name": "response", "schema": json.RawMessage(schema.Raw)}}))
		} else {
			req.Params.ResponseFormat = json.RawMessage(`{"type":"json_object"}`)
		}
	}
	if th := gc.Get("thinkingConfig"); th.Exists() {
		switch {
		case th.Get("thinkingBudget").Exists():
			b := int(th.Get("thinkingBudget").Int())
			if b == 0 {
				req.Thinking = translate.Thinking{Mode: "none"}
			} else if b < 0 {
				req.Thinking = translate.Thinking{Mode: "auto"}
			} else {
				req.Thinking = translate.Thinking{Mode: "budget", Budget: b}
			}
		case th.Get("thinkingLevel").Exists():
			req.Thinking = translate.Thinking{Mode: "level", Level: strings.ToLower(th.Get("thinkingLevel").String())}
		}
	}
	req.Extra = translate.ExtraOf(body, geminiKnown)
	return req, nil
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
func (GeminiCodec) WriteRequest(r *translate.Request, c *translate.Ctx) ([]byte, error) {
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
		case translate.RoleSystem:
			if t := m.Text(); strings.TrimSpace(t) != "" {
				system = append(system, t)
			}
		case translate.RoleTool:
			id := m.ToolCallID
			if q := queues[id]; len(q) > 0 {
				id, queues[id] = q[0], q[1:]
			}
			name := cmp.Or(m.Name, names[m.ToolCallID], "tool")
			add("user", []any{translate.Obj{"functionResponse": translate.Obj{"id": id, "name": SanitizeGeminiName(name), "response": translate.Obj{"result": m.Text()}}}})
		case translate.RoleAssistant:
			var parts []any
			if m.Reasoning != "" {
				parts = append(parts, translate.Obj{"text": m.Reasoning, "thought": true})
			}
			for _, p := range m.Content {
				if p.Type == translate.PartText && strings.TrimSpace(p.Text) != "" {
					parts = append(parts, translate.Obj{"text": p.Text})
				}
			}
			first := true
			for _, tc := range m.ToolCalls {
				id := uniq(tc.ID)
				queues[tc.ID] = append(queues[tc.ID], id)
				fc := translate.Obj{"id": id, "name": SanitizeGeminiName(tc.Name), "args": translate.RawOrEmpty(tc.Arguments)}
				part := translate.Obj{"functionCall": fc}
				sig := c.SignatureFor(tc)
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
		turns = append([]geminiTurn{{Role: "user", Parts: []any{translate.Obj{"text": "..."}}}}, turns...)
	}
	if n := len(turns); n > 0 && turns[n-1].Role != "user" {
		turns = append(turns, geminiTurn{Role: "user", Parts: []any{translate.Obj{"text": "Continue."}}})
	}
	// A history that ends on a function call is answered with a function response.
	if len(turns) == 0 {
		turns = []geminiTurn{{Role: "user", Parts: []any{translate.Obj{"text": "..."}}}}
	}

	o := translate.Obj{"contents": turns}
	if len(system) > 0 {
		o["systemInstruction"] = translate.Obj{"role": "user", "parts": []any{translate.Obj{"text": strings.Join(system, "\n")}}}
	}
	if len(r.Tools) > 0 {
		var decls []any
		for _, t := range r.Tools {
			if len(t.Raw) > 0 {
				continue
			}
			decls = append(decls, translate.Obj{"name": SanitizeGeminiName(t.Name), "description": t.Description, "parameters": CleanGeminiSchema(t.Parameters)})
		}
		if len(decls) > 0 {
			o["tools"] = []any{translate.Obj{"functionDeclarations": decls}}
		}
	}
	if tc := r.ToolChoice; tc != nil {
		cfg := translate.Obj{}
		switch tc.Mode {
		case "auto":
			cfg["mode"] = "AUTO"
		case "none":
			cfg["mode"] = "NONE"
		case "required":
			cfg["mode"] = "ANY"
		case "tool":
			cfg["mode"] = "ANY"
			cfg["allowedFunctionNames"] = []string{SanitizeGeminiName(tc.Name)}
		}
		o["toolConfig"] = translate.Obj{"functionCallingConfig": cfg}
	}
	if gc := geminiGenerationConfig(r, c); len(gc) > 0 {
		o["generationConfig"] = gc
	}
	if c.Safety {
		o["safetySettings"] = safetyOff
	}
	return json.Marshal(translate.WithExtra(o, r.Extra))
}

// safetyOff turns every harm filter off, the setting Gemini CLI sends.
var safetyOff = []any{
	translate.Obj{"category": "HARM_CATEGORY_HATE_SPEECH", "threshold": "OFF"},
	translate.Obj{"category": "HARM_CATEGORY_DANGEROUS_CONTENT", "threshold": "OFF"},
	translate.Obj{"category": "HARM_CATEGORY_SEXUALLY_EXPLICIT", "threshold": "OFF"},
	translate.Obj{"category": "HARM_CATEGORY_HARASSMENT", "threshold": "OFF"},
	translate.Obj{"category": "HARM_CATEGORY_CIVIC_INTEGRITY", "threshold": "OFF"},
}

func geminiContent(parts []translate.Part) []any {
	var out []any
	for _, p := range parts {
		switch p.Type {
		case translate.PartText:
			if strings.TrimSpace(p.Text) != "" {
				out = append(out, translate.Obj{"text": p.Text})
			}
		case translate.PartImage, translate.PartAudio, translate.PartFile:
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
				out = append(out, translate.Obj{"inlineData": translate.Obj{"mimeType": mt, "data": data}})
			} else if p.URL != "" {
				// A remote image must name a concrete type: the upstream refuses a
				// wildcard, so the URL's own suffix decides.
				if p.Type == translate.PartImage && (mt == "" || mt == "image/*") {
					mt = remoteImageMimeType(p.URL)
				}
				out = append(out, translate.Obj{"fileData": translate.Obj{"mimeType": mt, "fileUri": p.URL}})
			}
		}
	}
	return out
}

// remoteImageMimeType reads an image's type from its address, with PNG as the
// fallback the reference uses.
func remoteImageMimeType(raw string) string {
	path := raw
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}
	switch strings.ToLower(path[strings.LastIndexByte(path, '.')+1:]) {
	case "jpg", "jpeg":
		return "image/jpeg"
	case "webp":
		return "image/webp"
	case "gif":
		return "image/gif"
	case "bmp":
		return "image/bmp"
	}
	return "image/png"
}

func geminiGenerationConfig(r *translate.Request, c *translate.Ctx) translate.Obj {
	gc := translate.Obj{}
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
				gc["responseSchema"] = CleanGeminiSchema(rf.JSONSchema.Schema)
			}
		}
	}
	if th := r.Thinking; th.Set() {
		cfg := translate.Obj{"includeThoughts": true}
		switch th.Mode {
		case "none":
			cfg = translate.Obj{"thinkingBudget": 0}
		case "auto":
			cfg["thinkingBudget"] = -1
		case "budget":
			cfg["thinkingBudget"] = th.Budget
		case "level":
			if c.Caps.ThinkingFormat == "gemini-level" {
				cfg["thinkingLevel"] = strings.ToUpper(geminiLevel(th.Level, c.Levels))
			} else {
				cfg["thinkingBudget"] = c.Budget(th.Level)
			}
		}
		gc["thinkingConfig"] = cfg
	}
	return gc
}

// ReadResponse reads a generateContent answer.
func (GeminiCodec) ReadResponse(body []byte, c *translate.Ctx) (*translate.Response, error) {
	if !translate.ValidObject(body) {
		return nil, translate.ErrBadBody
	}
	g := gjson.ParseBytes(body)
	resp := &translate.Response{ID: g.Get("responseId").String(), Model: g.Get("modelVersion").String()}
	msg := translate.Message{Role: translate.RoleAssistant}
	cand := g.Get("candidates.0")
	for _, p := range cand.Get("content.parts").Array() {
		switch {
		case p.Get("functionCall").Exists():
			fc := p.Get("functionCall")
			id := fc.Get("id").String()
			if id == "" {
				id = fmt.Sprintf("call_%d_%s", len(msg.ToolCalls)+1, translate.CleanName(fc.Get("name").String()))
			}
			args := fc.Get("args").Raw
			if args == "" {
				args = "{}"
			}
			tc := translate.ToolCall{ID: id, Name: fc.Get("name").String(), Arguments: args, Signature: p.Get("thoughtSignature").String()}
			if tc.Signature != "" && c.Calls != nil {
				c.Calls.Put(id, tc.Signature)
			}
			msg.ToolCalls = append(msg.ToolCalls, tc)
		case p.Get("thought").Bool():
			msg.Reasoning += p.Get("text").String()
		case p.Get("text").Exists():
			msg.Content = append(msg.Content, translate.Part{Type: translate.PartText, Text: p.Get("text").String()})
		}
	}
	resp.Message = msg
	resp.FinishReason = translate.FinishWithCalls(translate.FinishFromGemini(cand.Get("finishReason").String()), len(msg.ToolCalls))
	resp.Usage = translate.ReadGeminiUsage(g.Get("usageMetadata"))
	resp.Extra = translate.ExtraOf(body, translate.Set("candidates", "usageMetadata", "modelVersion", "responseId"))
	return resp, nil
}

// WriteResponse writes the hub answer as a generateContent body.
func (GeminiCodec) WriteResponse(r *translate.Response, c *translate.Ctx) ([]byte, error) {
	var parts []any
	if r.Message.Reasoning != "" {
		parts = append(parts, translate.Obj{"text": r.Message.Reasoning, "thought": true})
	}
	for _, p := range r.Message.Content {
		if p.Type == translate.PartText {
			parts = append(parts, translate.Obj{"text": p.Text})
		}
	}
	for _, tc := range r.Message.ToolCalls {
		part := translate.Obj{"functionCall": translate.Obj{"id": tc.ID, "name": tc.Name, "args": translate.RawOrEmpty(tc.Arguments)}}
		if sig := c.SignatureFor(tc); sig != "" {
			part["thoughtSignature"] = sig
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		parts = []any{translate.Obj{"text": ""}}
	}
	u := translate.Obj{"promptTokenCount": r.Usage.Prompt, "candidatesTokenCount": r.Usage.Completion - r.Usage.Reasoning, "totalTokenCount": r.Usage.Prompt + r.Usage.Completion}
	if r.Usage.Reasoning > 0 {
		u["thoughtsTokenCount"] = r.Usage.Reasoning
	}
	if r.Usage.Cached > 0 {
		u["cachedContentTokenCount"] = r.Usage.Cached
	}
	o := translate.Obj{
		"candidates":    []any{translate.Obj{"content": translate.Obj{"role": "model", "parts": parts}, "finishReason": translate.FinishToGemini(r.FinishReason), "index": 0}},
		"usageMetadata": u, "modelVersion": r.Model, "responseId": r.ID,
	}
	return json.Marshal(translate.WithExtra(o, r.Extra))
}

// geminiLevel clamps a requested thinking level into the set the model serves,
// because Gemini answers a level outside it with a 400. xhigh stays where it is
// served and otherwise falls to the highest level below it; max is not a Gemini
// level and reads as the highest the model has; a level below the floor rises to
// it. With no set known the level is sent as it came.
func geminiLevel(level string, served []string) string {
	level = strings.ToLower(strings.TrimSpace(level))
	served = slices.DeleteFunc(slices.Clone(served), func(l string) bool { return l == "none" })
	if len(served) == 0 || slices.Contains(served, level) {
		return level
	}
	order := []string{"minimal", "low", "medium", "high", "xhigh"}
	if level == "max" {
		return served[len(served)-1]
	}
	at := slices.Index(order, level)
	if at < 0 {
		return served[0]
	}
	for i := at; i >= 0; i-- {
		if slices.Contains(served, order[i]) {
			return order[i]
		}
	}
	return served[0]
}
