package pipeline

import "testing"

func TestAcceptJSONTurnsOffOnlyADefaultStream(t *testing.T) {
	cases := []struct {
		name, path, body string
		acceptJSON       bool
		want             bool
	}{
		{"ollama default streams", "/v1/api/chat", `{"model":"m","messages":[{"role":"user","content":"hi"}]}`, false, true},
		{"ollama with accept json", "/v1/api/chat", `{"model":"m","messages":[{"role":"user","content":"hi"}]}`, true, false},
		{"ollama explicit stream wins", "/v1/api/chat", `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`, true, true},
		{"openai explicit stream wins", "/v1/chat/completions", `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`, true, true},
		{"claude explicit stream wins", "/v1/messages", `{"model":"m","max_tokens":5,"stream":true,"messages":[{"role":"user","content":"hi"}]}`, true, true},
		{"gemini url action wins", "/v1beta/models/m:streamGenerateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`, true, true},
	}
	for _, c := range cases {
		call := &call{in: Input{Path: c.path, Body: []byte(c.body), AcceptJSON: c.acceptJSON}}
		if _, ok := call.parse(); !ok {
			t.Errorf("%s: parse failed", c.name)
			continue
		}
		if call.clientStream != c.want {
			t.Errorf("%s: stream = %v, want %v", c.name, call.clientStream, c.want)
		}
	}
}
