package translate

import "testing"

func TestCountTokens(t *testing.T) {
	read := func(body string) *Request {
		t.Helper()
		req, err := (ClaudeCodec{}).ReadRequest([]byte(body), NewCtx(FormatClaude, FormatClaude))
		if err != nil {
			t.Fatal(err)
		}
		return req
	}
	cases := []struct {
		name, body string
		want       int64
	}{
		{"ascii", `{"model":"x","messages":[{"role":"user","content":"hello world"}]}`, 3},
		{"multi-byte counts runes", `{"model":"x","messages":[{"role":"user","content":"日本語日本語日本語日本語"}]}`, 3},
		{"system", `{"model":"x","system":"abcd","messages":[{"role":"user","content":"abcd"}]}`, 2},
		{"tool", `{"model":"x","messages":[{"role":"user","content":""}],"tools":[{"name":"ab","description":"cd","input_schema":{}}]}`, 2},
		{"tool use and result", `{"model":"x","messages":[{"role":"assistant","content":[{"type":"tool_use","id":"1","name":"ab","input":{"a":1}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"1","content":"abcdefgh"}]}]}`, 5},
		{"image adds nothing", `{"model":"x","messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAAAAAAAAAAAAAAAAAAAAAA"}}]}]}`, 0},
	}
	for _, c := range cases {
		if got := CountTokens(read(c.body)); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}
