package translate

import (
	"fmt"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// renameHistoryReference is the one-edit-per-block version renameHistory
// replaced; it copies the whole body for each tool call.
func renameHistoryReference(body []byte) []byte {
	msgs := gjson.GetBytes(body, "messages")
	if !msgs.IsArray() {
		return body
	}
	for i, m := range msgs.Array() {
		content := m.Get("content")
		if !content.IsArray() {
			continue
		}
		for j, b := range content.Array() {
			if b.Get("type").String() != "tool_use" {
				continue
			}
			path := fmt.Sprintf("messages.%d.content.%d.name", i, j)
			if next, err := sjson.SetBytes(body, path, b.Get("name").String()+toolSuffix); err == nil {
				body = next
			}
		}
	}
	return body
}

func TestRenameHistoryMatchesThePerBlockEdit(t *testing.T) {
	cases := map[string]string{
		"no messages":  `{"model":"m"}`,
		"not an array": `{"messages":"x"}`,
		"empty":        `{"messages":[]}`,
		"no tool use":  `{"messages":[{"role":"user","content":"hi"},{"role":"user","content":[{"type":"text","text":"a"}]}]}`,
		"one call":     `{"a":1,"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"t","name":"Read","input":{"p":1}}]}],"z":2}`,
		"mixed": `{"messages":[{"role":"user","content":"q"},{"role":"assistant","content":[{"type":"text","text":"t"},` +
			`{"type":"tool_use","id":"1","name":"Bash","input":{}},{"type":"tool_use","id":"2","name":"Edit","input":{"n":[1,2,{"x":"y"}]}}]},` +
			`{"role":"user","content":[{"type":"tool_result","tool_use_id":"1","content":"ok"}]}]}`,
		"odd spacing":      "{ \"messages\" : [ { \"content\" : [ {\"type\":\"tool_use\",\"name\":\"A\"} ] } ] }",
		"name with quotes": `{"messages":[{"content":[{"type":"tool_use","name":"a\"b"}]}]}`,
		"unicode":          `{"messages":[{"content":[{"type":"tool_use","name":"\u00e9t\u00e9","input":{"k":"\ud83d\ude00"}}]}]}`,
		"empty name":       `{"messages":[{"content":[{"type":"tool_use","name":""}]}]}`,
		"big numbers":      `{"messages":[{"content":[{"type":"tool_use","name":"n","input":{"big":12345678901234567890.5e3}}]}],"max":1e400}`,
	}
	for name, in := range cases {
		want := string(renameHistoryReference([]byte(in)))
		got := string(renameHistory([]byte(in)))
		if got != want {
			t.Errorf("%s:\n got  %s\n want %s", name, got, want)
		}
	}
}

func TestRenameHistorySuffixesEveryCall(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"messages":[`)
	for i := range 50 {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"role":"assistant","content":[{"type":"tool_use","id":"%d","name":"T%d","input":{}}]}`, i, i)
	}
	b.WriteString(`]}`)
	out := renameHistory([]byte(b.String()))
	for i := range 50 {
		if got := gjson.GetBytes(out, fmt.Sprintf("messages.%d.content.0.name", i)).String(); got != fmt.Sprintf("T%d%s", i, toolSuffix) {
			t.Fatalf("call %d is %q", i, got)
		}
	}
}

func FuzzRenameHistoryMatchesThePerBlockEdit(f *testing.F) {
	f.Add(`{"messages":[{"content":[{"type":"tool_use","name":"a"}]}]}`)
	f.Add(`{"messages":[{"content":"x"},{"content":[{"type":"tool_use","name":"b"},{"type":"tool_use","name":"c"}]}]}`)
	f.Fuzz(func(t *testing.T, in string) {
		if !gjson.Valid(in) {
			return
		}
		want := string(renameHistoryReference([]byte(in)))
		if got := string(renameHistory([]byte(in))); got != want {
			t.Fatalf("%q:\n got  %s\n want %s", in, got, want)
		}
	})
}
