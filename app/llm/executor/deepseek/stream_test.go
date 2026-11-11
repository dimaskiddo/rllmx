package deepseek

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// chunksOf runs one event through a fresh turn and returns the SSE body.
func chunksOf(t *testing.T, model string, events ...string) string {
	t.Helper()
	var body strings.Builder
	for _, e := range events {
		body.WriteString(e)
		body.WriteString("\n\n")
	}
	tr := &turn{
		src: bufio.NewReaderSize(strings.NewReader(body.String()), maxLine), model: model,
		thinking: strings.Contains(strings.ToLower(model), "think"),
		search:   strings.Contains(strings.ToLower(model), "search"),
		quiet:    strings.Contains(strings.ToLower(model), "search-silent"),
		now:      1, nextID: "chatcmpl-x",
	}
	out, err := io.ReadAll(tr)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(out)
}

func TestStreamFollowsTheFragmentTypes(t *testing.T) {
	out := chunksOf(t, "deepseek-chat",
		`data: {"p":"response/fragments","v":[{"type":"THINK","content":"plan"}]}`,
		`data: {"p":"response/fragments","v":[{"type":"ANSWER","content":"hi"}]}`,
		`data: {"p":"response/status","v":"FINISHED"}`,
	)
	if !strings.Contains(out, `"reasoning_content":"plan"`) || !strings.Contains(out, `"content":"hi"`) {
		t.Fatalf("answer = %s", out)
	}
	if !strings.Contains(out, `"role":"assistant"`) || !strings.Contains(out, `"finish_reason":"stop"`) || !strings.Contains(out, "data: [DONE]") {
		t.Fatalf("the turn did not end: %s", out)
	}
}

func TestStreamStripsTheProgressWord(t *testing.T) {
	out := chunksOf(t, "deepseek-chat", `data: {"v":"SEARCHING FINISHED the web"}`)
	if strings.Contains(out, "SEARCHING") || strings.Contains(out, "FINISHED") {
		t.Fatalf("answer = %s", out)
	}
	if !strings.Contains(out, `"content":"the web"`) {
		t.Fatalf("answer = %s", out)
	}
}

func TestAThinkingModelPutsUntypedTextInItsThinking(t *testing.T) {
	out := chunksOf(t, "deepseek-v4-pro-think", `data: {"v":"pondering"}`)
	if !strings.Contains(out, `"reasoning_content":"pondering"`) {
		t.Fatalf("answer = %s", out)
	}
}

func TestASearchAnswerKeepsItsCitations(t *testing.T) {
	out := chunksOf(t, "DeepSeek-Search",
		`data: {"p":"response/search_results","v":[{"cite_index":2,"title":"B","url":"https://b.example"},{"cite_index":1,"title":"A","url":"https://a.example"}]}`,
		`data: {"v":"see [citation:1]"}`,
		`data: {"p":"response/status","v":"FINISHED"}`,
	)
	if !strings.Contains(out, "see [1]") {
		t.Fatalf("the marker was not rewritten: %s", out)
	}
	if !strings.Contains(out, "[1]: [A](https://a.example)") || !strings.Contains(out, "[2]: [B](https://b.example)") {
		t.Fatalf("citations = %s", out)
	}
}

func TestASilentSearchAnswerDropsItsMarkers(t *testing.T) {
	out := chunksOf(t, "deepseek-search-silent",
		`data: {"p":"response/search_results","v":[{"cite_index":1,"title":"A","url":"https://a.example"}]}`,
		`data: {"v":"see [citation:1]"}`,
	)
	if strings.Contains(out, "citation:") || strings.Contains(out, "https://a.example") {
		t.Fatalf("answer = %s", out)
	}
}

func TestAnEmptyTurnStillEnds(t *testing.T) {
	out := chunksOf(t, "deepseek-chat")
	if !strings.Contains(out, `"finish_reason":"stop"`) || !strings.Contains(out, "data: [DONE]") {
		t.Fatalf("answer = %s", out)
	}
}

func TestPromptShapes(t *testing.T) {
	parse := func(raw string) []gjson.Result { return gjson.Get(raw, "messages").Array() }
	cases := []struct {
		name   string
		body   string
		window int
		want   string
	}{
		{"one turn", `{"messages":[{"role":"system","content":"be brief"},{"role":"user","content":"hi"}]}`, 0, "be brief\n\nhi"},
		{"window of two", `{"messages":[
			{"role":"user","content":"one"},{"role":"assistant","content":"two"},
			{"role":"user","content":"three"},{"role":"assistant","content":"four"}]}`, 2,
			"user: three\n\nassistant: four"},
		{"tool result folded", `{"messages":[
			{"role":"user","content":"go"},
			{"role":"assistant","content":"","tool_calls":[{"id":"c1","function":{"name":"read","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"c1","content":"file"}]}`, 0,
			"user: go\n\nTool result (read) file"},
		{"image link stripped", `{"messages":[{"role":"user","content":"look ![x](https://x/y.png) here"}]}`, 0, "look  here"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := prompt(parse(c.body), c.window); got != c.want {
				t.Fatalf("prompt = %q, want %q", got, c.want)
			}
		})
	}
}

func TestOptionsReadTheModelAndBody(t *testing.T) {
	o := optionsOf("deepseek-v4-pro-think-search", gjson.Parse(`{}`))
	if o.modelType != "expert" || !o.thinking || !o.search {
		t.Fatalf("options = %+v", o)
	}
	o = optionsOf("deepseek-chat", gjson.Parse(`{"reasoning_effort":"high","search_enabled":true}`))
	if o.modelType != "default" || !o.thinking || !o.search {
		t.Fatalf("options = %+v", o)
	}
}

func TestChallengeValidation(t *testing.T) {
	good := `{"data":{"biz_data":{"challenge":{"algorithm":"DeepSeekHashV1","challenge":"` +
		strings.Repeat("ab", 32) + `","salt":"s","signature":"x","target_path":"/p","difficulty":10,"expire_at":1}}}}`
	if _, ok := parseChallenge([]byte(good)); !ok {
		t.Fatal("a good challenge was refused")
	}
	bad := []string{
		`{}`,
		strings.Replace(good, "DeepSeekHashV1", "Other", 1),
		strings.Replace(good, strings.Repeat("ab", 32), "zz", 1),
		strings.Replace(good, `"difficulty":10`, `"difficulty":0`, 1),
		strings.Replace(good, `"difficulty":10`, `"difficulty":250001`, 1),
		strings.Replace(good, `"salt":"s"`, `"salt":""`, 1),
	}
	for _, raw := range bad {
		if _, ok := parseChallenge([]byte(raw)); ok {
			t.Errorf("accepted %s", raw)
		}
	}
}

func TestSolveFindsTheNonceAndStopsOnACancelledContext(t *testing.T) {
	c, ok := parseChallenge([]byte(`{"data":{"biz_data":{"challenge":{"algorithm":"DeepSeekHashV1","challenge":"` +
		strings.Repeat("00", 32) + `","salt":"s","signature":"x","target_path":"/p","difficulty":5000,"expire_at":1}}}}`))
	if !ok {
		t.Fatal("the challenge was refused")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, found := solve(ctx, c); found {
		t.Fatal("a cancelled context kept searching")
	}
	// A real challenge at a small nonce is found.
	real := challengeFor(t, 3)
	parsed, ok := parseChallenge([]byte(mustJSON(map[string]any{"data": map[string]any{"biz_data": map[string]any{"challenge": real}}})))
	if !ok {
		t.Fatal("the generated challenge was refused")
	}
	nonce, found := solve(context.Background(), parsed)
	if !found || nonce != 3 {
		t.Fatalf("nonce = %d, %v", nonce, found)
	}
}

func TestAnswerIsBase64JSON(t *testing.T) {
	c, _ := parseChallenge([]byte(`{"data":{"biz_data":{"challenge":{"algorithm":"DeepSeekHashV1","challenge":"` +
		strings.Repeat("ab", 32) + `","salt":"s","signature":"x","target_path":"/p","difficulty":10,"expire_at":1}}}}`))
	raw, err := answer(c, 5)
	if err != nil {
		t.Fatalf("answer: %v", err)
	}
	decoded, err := base64Decode(raw)
	if err != nil {
		t.Fatalf("not base64: %v", err)
	}
	if !strings.Contains(decoded, `"answer":5`) || !strings.Contains(decoded, `"salt":"s"`) {
		t.Fatalf("proof = %s", decoded)
	}
}

func FuzzTurn(f *testing.F) {
	f.Add(`data: {"p":"response/fragments","v":[{"type":"ANSWER","content":"hi"}]}`)
	f.Add(`data: {"p":"response/search_results","v":[{"cite_index":1,"url":"u","title":"t"}]}`)
	f.Add(`data: {"v":["a",{"content":"b"}]}`)
	f.Fuzz(func(t *testing.T, event string) {
		tr := &turn{src: bufio.NewReaderSize(strings.NewReader("data: "+event+"\n\n"), maxLine), model: "deepseek-search"}
		buf := make([]byte, 4096)
		for i := 0; i < 16; i++ {
			if _, err := tr.Read(buf); err != nil {
				return
			}
		}
	})
}

// mustJSON encodes a value for a test.
func mustJSON(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

// base64Decode decodes a proof for a test.
func base64Decode(s string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(s)
	return string(raw), err
}
