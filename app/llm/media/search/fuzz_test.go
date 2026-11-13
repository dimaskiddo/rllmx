package search

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
)

// FuzzTheGLMEnvelope reads the tool's answer, whose payload is JSON inside a
// string: any bytes must leave either an answer or a refusal, never a panic.
func FuzzTheGLMEnvelope(f *testing.F) {
	f.Add([]byte(`{"result":{"content":[{"type":"text","text":"{\"results\":[{\"title\":\"a\",\"link\":\"https://a.test\"}]}"}]}}`))
	f.Add([]byte(`{"result":{"content":[{"text":"{"}]}}`))
	f.Add([]byte(`[]`))
	f.Fuzz(func(t *testing.T, data []byte) {
		r := media.Request{Kind: upstream.KindSearch, Provider: "glm", Body: []byte(`{"query":"q","max_results":3}`)}
		out, err := (GLM{}).Parse(context.Background(), r, nil, 200, nil, data)
		if err != nil {
			return
		}
		if !gjson.ValidBytes(out.Body) {
			t.Fatalf("not JSON: %s", out.Body)
		}
	})
}
