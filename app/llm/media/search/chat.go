package search

import (
	"context"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
)

// The Antigravity request the IDE sends for a grounded answer.
const (
	antigravityAgent        = "antigravity"
	antigravityTemperature  = 1.0
	antigravityMaxOutput    = 8192
	antigravityContextBack  = 150
	antigravityContextAhead = 250
)

// Chat searches through a provider's own chat endpoint with the search tool it
// offers, and reads the answer and its citations back.
type Chat struct{}

// Kind is the service it serves.
func (Chat) Kind() string { return upstream.KindSearch }

// Build makes the search call for the provider's own shape.
func (Chat) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	if r.Credential == "" {
		return media.Call{}, media.BadRequestf("this provider needs a credential")
	}
	sv := r.Entry.Media.SearchViaChat
	if sv == nil || sv.DefaultModel == "" {
		return media.Call{}, media.BadRequestf("this provider names no search model")
	}
	target := strings.ReplaceAll(sv.Endpoint, "{model}", sv.DefaultModel)
	if target == "" {
		target = r.Entry.Transport.BaseURL
	}
	if target == "" || !strings.HasPrefix(target, "https://") && !strings.HasPrefix(target, "http://") {
		return media.Call{}, media.BadRequestf("this provider has no address for a search")
	}
	payload, header, err := chatRequest(r, sv.DefaultModel, field(r, "query"))
	if err != nil {
		return media.Call{}, err
	}
	return call(http.MethodPost, target, header, payload)
}

// chatRequest builds the body and headers of one provider's chat search.
func chatRequest(r media.Request, model, query string) (any, map[string]string, error) {
	bearer := func() map[string]string {
		return map[string]string{"Content-Type": "application/json", "Authorization": "Bearer " + r.Credential}
	}
	user := func() []any { return []any{map[string]any{"role": "user", "content": query}} }
	switch r.Provider {
	case "gemini":
		return map[string]any{
			"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": query}}}},
			"tools":    []any{map[string]any{"google_search": map[string]any{}}},
		}, map[string]string{"Content-Type": "application/json", "x-goog-api-key": r.Credential}, nil
	case "antigravity":
		project := r.Account["projectId"]
		if project == "" {
			return nil, nil, media.BadRequestf("this account has no project; reconnect it")
		}
		h := bearer()
		if ua := r.Entry.Transport.Headers["User-Agent"]; ua != "" {
			h["User-Agent"] = ua
		}
		return map[string]any{
			"project": project, "model": model, "userAgent": antigravityAgent, "requestType": "search",
			"request": map[string]any{
				"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": query}}}},
				"tools":    []any{map[string]any{"googleSearch": map[string]any{}}},
				"generationConfig": map[string]any{
					"temperature": antigravityTemperature, "maxOutputTokens": antigravityMaxOutput,
				},
			},
		}, h, nil
	case "openai":
		payload := map[string]any{"model": model, "messages": user()}
		if !strings.Contains(strings.ToLower(model), "search") {
			payload["tools"] = []any{map[string]any{"type": "web_search"}}
		}
		return payload, bearer(), nil
	case "xai":
		return map[string]any{
			"model": model, "input": []any{map[string]any{"role": "user", "content": query}},
			"tools": []any{map[string]any{"type": "web_search"}},
		}, bearer(), nil
	case "kimi":
		return map[string]any{
			"model": model, "messages": user(),
			"tools": []any{map[string]any{"type": "builtin_function", "function": map[string]any{"name": "$web_search"}}},
		}, bearer(), nil
	case "minimax":
		return map[string]any{
			"model": model, "messages": user(),
			"tools": []any{map[string]any{"type": "web_search"}},
		}, bearer(), nil
	case "perplexity":
		return map[string]any{"model": model, "messages": user()}, bearer(), nil
	case "perplexity-agent":
		return map[string]any{
			"model": model, "input": query,
			"tools": []any{map[string]any{"type": "web_search"}},
		}, bearer(), nil
	}
	return nil, nil, media.BadRequestf("this provider has no chat search shape")
}

// Parse reads the answer and its citations.
func (Chat) Parse(_ context.Context, r media.Request, _ media.Client, status int, _ http.Header, raw []byte) (media.Response, error) {
	if status < 200 || status > 299 {
		return media.Response{}, refused(status)
	}
	var text string
	var cites []result
	model := ""
	if sv := r.Entry.Media.SearchViaChat; sv != nil {
		model = sv.DefaultModel
	}
	switch r.Provider {
	case "gemini":
		text, cites = geminiAnswer(gjson.ParseBytes(raw), r.Provider)
	case "antigravity":
		response := gjson.GetBytes(raw, "response")
		if !response.Exists() {
			response = gjson.ParseBytes(raw)
		}
		text, cites = antigravityAnswer(response, r.Provider)
	case "xai", "perplexity-agent":
		text, cites = responsesAnswer(gjson.ParseBytes(raw), r.Provider)
	case "kimi":
		text, cites = toolCallsAnswer(gjson.ParseBytes(raw), r.Provider)
	case "minimax":
		text, cites = minimaxAnswer(gjson.ParseBytes(raw), r.Provider)
	default:
		text, cites = chatCompletionsAnswer(gjson.ParseBytes(raw), r.Provider)
	}
	if limit := count(r, "max_results"); len(cites) > limit && limit > 0 {
		cites = cites[:limit]
	}
	ans := &answer{Source: r.Provider, Text: text, Model: model}
	return reply(r.Provider, field(r, "query"), cites, ans, usage{QueriesUsed: 1, SearchCost: free()}, nil, nil)
}

// geminiAnswer reads a Gemini answer with its grounding chunks.
func geminiAnswer(data gjson.Result, provider string) (string, []result) {
	candidate := data.Get("candidates.0")
	text := partsText(candidate.Get("content.parts"))
	out := []result{}
	for i, chunk := range candidate.Get("groundingMetadata.groundingChunks").Array() {
		web := chunk.Get("web")
		link := firstOf(web.Get("uri").String(), web.Get("url").String())
		if link == "" {
			continue
		}
		out = append(out, item(provider, i+1, web.Get("title").String(), link))
	}
	return text, out
}

// antigravityAnswer reads an Antigravity answer, widening each grounded sentence
// to its surroundings and keeping one result per source.
func antigravityAnswer(data gjson.Result, provider string) (string, []result) {
	candidate := data.Get("candidates.0")
	text := partsText(candidate.Get("content.parts"))
	chunks := candidate.Get("groundingMetadata.groundingChunks").Array()

	type source struct {
		title, link string
		snippets    []string
		contexts    []string
	}
	index := map[int]*source{}
	order := []int{}
	for i, chunk := range chunks {
		web := chunk.Get("web")
		link := firstOf(web.Get("uri").String(), web.Get("url").String())
		if link == "" {
			continue
		}
		index[i] = &source{title: web.Get("title").String(), link: link}
		order = append(order, i)
	}
	for _, support := range candidate.Get("groundingMetadata.groundingSupports").Array() {
		segment := support.Get("segment")
		grounded := segment.Get("text").String()
		widened := widen(text, int(segment.Get("startIndex").Int()), int(segment.Get("endIndex").Int()))
		for _, at := range support.Get("groundingChunkIndices").Array() {
			src := index[int(at.Int())]
			if src == nil {
				continue
			}
			if grounded != "" {
				src.snippets = appendUnique(src.snippets, grounded)
			}
			if widened != "" {
				src.contexts = appendUnique(src.contexts, widened)
			}
		}
	}
	out := []result{}
	for _, i := range order {
		src := index[i]
		snippet := firstOf(strings.Join(src.snippets, " | "), src.title)
		out = append(out, item(provider, len(out)+1, src.title, src.link).
			withSnippet(snippet).
			withContent("text", firstOf(strings.Join(src.contexts, "\n\n"), snippet)))
	}
	return text, out
}

// responsesAnswer reads an answer from a Responses-shaped body.
func responsesAnswer(data gjson.Result, provider string) (string, []result) {
	var text strings.Builder
	out := []result{}
	for _, block := range data.Get("output").Array() {
		for _, part := range block.Get("content").Array() {
			if part.Get("text").Type == gjson.String {
				text.WriteString(part.Get("text").String())
			}
			for _, ann := range part.Get("annotations").Array() {
				cited := ann.Get("url_citation")
				if !cited.Exists() {
					cited = ann
				}
				if link := cited.Get("url").String(); link != "" {
					out = append(out, item(provider, len(out)+1, cited.Get("title").String(), link))
				}
			}
		}
		for _, hit := range block.Get("results").Array() {
			link := firstOf(hit.Get("url").String(), hit.Get("link").String())
			if link == "" {
				continue
			}
			out = append(out, item(provider, len(out)+1, hit.Get("title").String(), link).
				withSnippet(hit.Get("snippet").String()))
		}
	}
	if len(out) == 0 {
		for _, c := range data.Get("citations").Array() {
			link := c.Get("url").String()
			if link == "" {
				link = c.String()
			}
			if link == "" {
				continue
			}
			out = append(out, item(provider, len(out)+1, c.Get("title").String(), link))
		}
	}
	return text.String(), out
}

// toolCallsAnswer reads citations a chat answer carries in a tool call.
func toolCallsAnswer(data gjson.Result, provider string) (string, []result) {
	message := data.Get("choices.0.message")
	out := []result{}
	for _, call := range message.Get("tool_calls").Array() {
		parsed := call.Get("function.arguments").String()
		if parsed == "" || !gjson.Valid(parsed) {
			continue
		}
		args := gjson.Parse(parsed)
		items := args.Get("search_results")
		if !items.Exists() {
			items = args.Get("results")
		}
		if !items.Exists() {
			items = args.Get("references")
		}
		for _, v := range items.Array() {
			link := firstOf(v.Get("url").String(), v.Get("link").String())
			if link == "" {
				continue
			}
			out = append(out, item(provider, len(out)+1, v.Get("title").String(), link).
				withSnippet(firstOf(v.Get("snippet").String(), v.Get("summary").String())))
		}
	}
	return message.Get("content").String(), out
}

// minimaxAnswer reads Minimax's own result list, then its tool call's.
func minimaxAnswer(data gjson.Result, provider string) (string, []result) {
	out := []result{}
	for _, v := range data.Get("web_search_results").Array() {
		link := firstOf(v.Get("url").String(), v.Get("link").String())
		if link == "" {
			continue
		}
		out = append(out, item(provider, len(out)+1, v.Get("title").String(), link).
			withSnippet(firstOf(v.Get("snippet").String(), v.Get("summary").String())))
	}
	message := data.Get("choices.0.message")
	if len(out) > 0 {
		return message.Get("content").String(), out
	}
	return toolCallsAnswer(data, provider)
}

// chatCompletionsAnswer reads a chat answer with its citations.
func chatCompletionsAnswer(data gjson.Result, provider string) (string, []result) {
	message := data.Get("choices.0.message")
	out := []result{}
	for _, ann := range message.Get("annotations").Array() {
		cited := ann.Get("url_citation")
		if !cited.Exists() {
			continue
		}
		if link := cited.Get("url").String(); link != "" {
			out = append(out, item(provider, len(out)+1, cited.Get("title").String(), link))
		}
	}
	if len(out) == 0 {
		for _, c := range data.Get("citations").Array() {
			link := c.Get("url").String()
			if link == "" && c.Type == gjson.String {
				link = c.String()
			}
			if link == "" {
				continue
			}
			out = append(out, item(provider, len(out)+1, c.Get("title").String(), link))
		}
	}
	return message.Get("content").String(), out
}

// partsText joins the text of an answer's parts.
func partsText(parts gjson.Result) string {
	var b strings.Builder
	for _, p := range parts.Array() {
		if p.Get("text").Type == gjson.String {
			b.WriteString(p.Get("text").String())
		}
	}
	return b.String()
}

// widen is the sentence around a grounded segment, with the words a window cut
// off marked by an ellipsis.
func widen(text string, start, end int) string {
	if start < 0 || end < start || end > len(text) {
		return ""
	}
	from, to := max(0, start-antigravityContextBack), min(len(text), end+antigravityContextAhead)
	out := strings.TrimSpace(text[from:to])
	if from > 0 {
		if i := strings.IndexAny(out, " \t\n"); i >= 0 {
			out = "..." + strings.TrimLeft(out[i:], " \t\n")
		}
	}
	if to < len(text) {
		if i := strings.LastIndexAny(out, " \t\n"); i >= 0 {
			out = strings.TrimRight(out[:i], " \t\n") + "..."
		}
	}
	return strings.TrimSpace(out)
}

func appendUnique(list []string, v string) []string {
	for _, have := range list {
		if have == v {
			return list
		}
	}
	return append(list, v)
}
