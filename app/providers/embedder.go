package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/tidwall/gjson"

	gatewaymedia "rllmx/app/gateway/media"
)

// embedder is the semantic cache's text-to-vector call. It goes through the
// media pipeline, so it uses a real connection, the guarded client and one
// usage row like any other request.
func embedder(src *MediaSource) func(ctx context.Context, model, text string) ([]float32, error) {
	return func(ctx context.Context, model, text string) ([]float32, error) {
		if src == nil {
			return nil, fmt.Errorf("embedder: no media pipeline")
		}
		p, err := src.Get()
		if err != nil {
			return nil, err
		}
		body, err := json.Marshal(map[string]any{"model": model, "input": text})
		if err != nil {
			return nil, err
		}
		out := p.Embeddings(ctx, gatewaymedia.Input{
			Path: "/v1/embeddings", Body: body, RequestID: "semantic-" + strconv.FormatInt(time.Now().UnixNano(), 10),
			APIKeyID: "semantic-cache", KeyName: "Semantic Cache",
		})
		if out.Status != 200 {
			return nil, fmt.Errorf("embedder: status %d", out.Status)
		}
		values := gjson.GetBytes(out.Body, "data.0.embedding").Array()
		if len(values) == 0 {
			return nil, fmt.Errorf("embedder: no vector")
		}
		vec := make([]float32, len(values))
		for i, v := range values {
			vec[i] = float32(v.Float())
		}
		return vec, nil
	}
}
