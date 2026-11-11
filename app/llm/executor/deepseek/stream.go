package deepseek

import (
	"bufio"
	"encoding/json"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// maxLine bounds one stream line; the endpoint sends small JSON patches.
const maxLine = 1 << 20

// citationPattern is how a search answer marks a source in its own text.
var citationPattern = regexp.MustCompile(`\[citation:(\d+)\]`)

// leadWord is the progress word the endpoint prefixes its search steps with.
var leadWord = regexp.MustCompile(`(?i)^(SEARCHING|WEB_SEARCH|SEARCH)\s*`)

// chunk is one OpenAI chat chunk this transcoder emits.
type chunk struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []choice `json:"choices"`
}

type choice struct {
	Index        int     `json:"index"`
	Delta        delta   `json:"delta"`
	FinishReason *string `json:"finish_reason"`
}

type delta struct {
	Role             string      `json:"role,omitempty"`
	Content          string      `json:"content,omitempty"`
	ReasoningContent string      `json:"reasoning_content,omitempty"`
	ToolCalls        []toolDelta `json:"tool_calls,omitempty"`
}

// toolDelta is one call in a chunk.
type toolDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}

// turn turns the endpoint's `{p,o,v}` events into OpenAI chat chunks. It is a
// pull reader, read by one goroutine, the way the pipeline reads any SSE answer.
type turn struct {
	src      *bufio.Reader
	model    string
	thinking bool
	search   bool
	quiet    bool

	path   string
	role   bool
	text   strings.Builder
	done   bool
	buf    []byte
	cites  []citation
	now    int64
	nextID string
}

// citation is one search source the answer cites.
type citation struct {
	index int
	title string
	url   string
}

// Read fills p from the chunks the stream produced.
func (t *turn) Read(p []byte) (int, error) {
	for len(t.buf) == 0 {
		if t.done {
			return 0, io.EOF
		}
		if err := t.step(); err != nil {
			if err != io.EOF {
				return 0, err
			}
			t.finish()
		}
	}
	n := copy(p, t.buf)
	t.buf = t.buf[n:]
	return n, nil
}

// Close releases the body; the reader holds nothing of its own.
func (t *turn) Close() error { return nil }

// step reads one line of the answer.
func (t *turn) step() error {
	line, err := t.src.ReadString('\n')
	if err != nil && line == "" {
		return err
	}
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "data:") {
		return nil
	}
	payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	if payload == "[DONE]" {
		t.finish()
		return nil
	}
	var event struct {
		P string          `json:"p"`
		O string          `json:"o"`
		V json.RawMessage `json:"v"`
	}
	if json.Unmarshal([]byte(payload), &event) != nil {
		return nil
	}
	t.apply(event.P, event.O, event.V)
	return nil
}

// apply reads one patch: a batch of fragments, a path switch, a citation list,
// or the turn's end.
func (t *turn) apply(p, op string, raw json.RawMessage) {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return
	}
	switch p {
	case "response/fragments":
		t.fragments(v)
	case "response":
		t.responseBatch(v)
	case "response/search_results":
		t.citations(op, v)
	case "response/status":
		if s, ok := v.(string); ok && s == "FINISHED" {
			t.finish()
		}
	case "response/search_status":
		return
	}
	if s, ok := v.(string); ok {
		t.send(t.clean(s))
	}
}

// responseBatch reads the answer's opening state and any batched fragments.
func (t *turn) responseBatch(v any) {
	list, ok := v.([]any)
	if !ok {
		t.fragments(v)
		return
	}
	for _, entry := range list {
		m, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if inner, ok := m["v"].(map[string]any); ok {
			if on, ok := inner["thinking_enabled"].(bool); ok {
				t.switchPath(on)
			}
			t.fragments(inner["fragments"])
			continue
		}
		if items, ok := m["v"].([]any); ok {
			t.joined(items)
		}
	}
}

// fragments reads a batch of {type, content} fragments.
func (t *turn) fragments(v any) {
	if m, ok := v.(map[string]any); ok {
		t.fragment(m)
		return
	}
	for _, entry := range toList(v) {
		if m, ok := entry.(map[string]any); ok {
			t.fragment(m)
		}
	}
}

// fragment sends one fragment's text down the path its type names.
func (t *turn) fragment(m map[string]any) {
	switch strings.ToUpper(text(m["type"])) {
	case "THINK":
		t.path = "thinking"
	case "ANSWER", "RESPONSE":
		t.path = "content"
	}
	if content, ok := m["content"].(string); ok {
		t.send(t.clean(content))
	}
}

// joined reads a batch of content items with no type of their own.
func (t *turn) joined(items []any) {
	var b strings.Builder
	for _, item := range items {
		if m, ok := item.(map[string]any); ok {
			b.WriteString(text(m["content"]))
		}
	}
	if b.Len() > 0 {
		t.send(t.clean(b.String()))
	}
}

// switchPath follows the answer's own statement about which half it writes.
func (t *turn) switchPath(thinking bool) {
	if thinking {
		t.path = "thinking"
		return
	}
	t.path = "content"
}

// citations collects the sources a search answer lists, or folds a late index
// change into one it already listed.
func (t *turn) citations(op string, v any) {
	if op == "BATCH" {
		for _, entry := range toList(v) {
			m, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			idx, ok := citeIndex(text(m["p"]))
			if !ok {
				continue
			}
			for i := range t.cites {
				if t.cites[i].index == idx {
					t.cites[i].index = number(m["v"], t.cites[i].index)
				}
			}
		}
		return
	}
	t.cites = t.cites[:0]
	for _, entry := range toList(v) {
		m, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		t.cites = append(t.cites, citation{index: number(m["cite_index"], 0), title: text(m["title"]), url: text(m["url"])})
	}
}

// citeIndex reads the index out of an `N/cite_index` path.
func citeIndex(path string) (int, bool) {
	head, tail, ok := strings.Cut(path, "/")
	if !ok || tail != "cite_index" || head == "" {
		return 0, false
	}
	n, err := strconv.Atoi(head)
	return n, err == nil
}

// send queues one delta on the current path.
func (t *turn) send(content string) {
	if content == "" {
		return
	}
	if t.path == "" {
		t.path = "content"
		if t.thinking {
			t.path = "thinking"
		}
	}
	d := delta{}
	if !t.role {
		t.role = true
		d.Role = "assistant"
	}
	if t.path == "thinking" {
		d.ReasoningContent = content
	} else {
		d.Content = content
		t.text.WriteString(content)
	}
	t.emit(d, nil)
}

// emit appends one chunk as an SSE frame.
func (t *turn) emit(d delta, finish *string) {
	raw, err := json.Marshal(chunk{
		ID: t.nextID, Object: "chat.completion.chunk", Created: t.now, Model: t.model,
		Choices: []choice{{Delta: d, FinishReason: finish}},
	})
	if err != nil {
		return
	}
	t.buf = append(t.buf, "data: "...)
	t.buf = append(t.buf, raw...)
	t.buf = append(t.buf, '\n', '\n')
}

// finish ends the turn: the sources a search answer lists, then the closing
// frame. It runs once.
func (t *turn) finish() {
	if t.done {
		return
	}
	t.done = true
	if cited := t.citationText(); cited != "" {
		t.send("\n\n" + cited)
	}
	if !t.role {
		t.role = true
		stop := "stop"
		t.emit(delta{Role: "assistant"}, &stop)
	} else {
		stop := "stop"
		t.emit(delta{}, &stop)
	}
	t.buf = append(t.buf, "data: [DONE]\n\n"...)
}

// citationText joins the collected sources, sorted by their number.
func (t *turn) citationText() string {
	if t.quiet || len(t.cites) == 0 {
		return ""
	}
	sort.SliceStable(t.cites, func(i, j int) bool { return t.cites[i].index < t.cites[j].index })
	var lines []string
	for _, c := range t.cites {
		if c.index == 0 || c.url == "" {
			continue
		}
		lines = append(lines, "["+strconv.Itoa(c.index)+"]: ["+c.title+"]("+c.url+")")
	}
	return strings.Join(lines, "\n")
}

// clean removes the endpoint's progress words and rewrites a citation marker
// into the markdown form the answer keeps.
func (t *turn) clean(s string) string {
	s = leadWord.ReplaceAllString(strings.ReplaceAll(s, "FINISHED", ""), "")
	if !t.search {
		return s
	}
	if t.quiet {
		return citationPattern.ReplaceAllString(s, "")
	}
	return citationPattern.ReplaceAllString(s, "[$1]")
}

// toList reads an array, or wraps any other value as a one-item list.
func toList(v any) []any {
	if list, ok := v.([]any); ok {
		return list
	}
	if v == nil {
		return nil
	}
	return []any{v}
}

// text reads a JSON string or "".
func text(v any) string {
	s, _ := v.(string)
	return s
}

// number reads a JSON number or the fallback.
func number(v any, fallback int) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return int(i)
		}
	}
	return fallback
}
