// Package google serves Gemini's and Vertex's media shapes: embeddings through
// embedContent, images through generateContent with an IMAGE response modality.
package google

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
)

// Embeddings speaks Gemini's embedContent body.
type Embeddings struct{}

// Kind is the service it serves.
func (Embeddings) Kind() string { return upstream.KindEmbedding }

// Build makes one call for a string input and a batch call for a list.
func (Embeddings) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	base := strings.TrimRight(media.BaseURL(r), "/")
	if base == "" {
		return media.Call{}, media.BadRequestf("the provider names no address")
	}
	in := gjson.GetBytes(r.Body, "input")
	var texts []string
	switch {
	case in.Type == gjson.String:
		texts = []string{in.String()}
	case in.IsArray():
		for _, v := range in.Array() {
			if v.Type != gjson.String {
				return media.Call{}, media.BadRequestf("the input holds no text")
			}
			texts = append(texts, v.String())
		}
	default:
		return media.Call{}, media.BadRequestf("the input is not text")
	}
	q := url.Values{}
	h := media.Headers(r, q)
	h["Content-Type"] = "application/json"

	model := "models/" + strings.TrimPrefix(r.WireModel, "models/")
	var (
		target string
		body   []byte
		err    error
	)
	if len(texts) == 1 {
		target = base + "/" + model + ":embedContent"
		body, err = json.Marshal(map[string]any{
			"model":   model,
			"content": map[string]any{"parts": []map[string]string{{"text": texts[0]}}},
		})
	} else {
		target = base + "/" + model + ":batchEmbedContents"
		requests := make([]map[string]any, 0, len(texts))
		for _, t := range texts {
			requests = append(requests, map[string]any{
				"model":   model,
				"content": map[string]any{"parts": []map[string]string{{"text": t}}},
			})
		}
		body, err = json.Marshal(map[string]any{"requests": requests})
	}
	if err != nil {
		return media.Call{}, media.BadRequestf("the request cannot be built")
	}
	if d := gjson.GetBytes(r.Body, "dimensions"); d.Type == gjson.Number {
		body, _ = setDimensions(body, d.Int(), len(texts))
	}
	return media.Call{Method: http.MethodPost, URL: withQuery(target, q), Header: h, Body: body}, nil
}

// Parse turns the vectors into the OpenAI list.
func (Embeddings) Parse(_ context.Context, r media.Request, _ media.Client, status int, _ http.Header, body []byte) (media.Response, error) {
	if status < 200 || status > 299 {
		return media.Response{}, &media.ProviderError{Status: status, Text: "the provider refused the embedding"}
	}
	data := []map[string]any{}
	if v := gjson.GetBytes(body, "embedding.values"); v.IsArray() {
		data = append(data, map[string]any{"object": "embedding", "index": 0, "embedding": v.Value()})
	}
	if gjson.GetBytes(body, "embeddings").IsArray() {
		for i, e := range gjson.GetBytes(body, "embeddings").Array() {
			data = append(data, map[string]any{"object": "embedding", "index": i, "embedding": e.Get("values").Value()})
		}
	}
	if len(data) == 0 {
		return media.Response{}, &media.ProviderError{Status: http.StatusBadGateway, Text: "the provider answered no vector"}
	}
	out, _ := json.Marshal(map[string]any{"object": "list", "data": data, "model": r.Model})
	return media.Response{Status: http.StatusOK, Header: map[string]string{"Content-Type": "application/json"}, Body: out}, nil
}

// Images asks Gemini for an image part.
type Images struct{}

// Kind is the service it serves.
func (Images) Kind() string { return upstream.KindImage }

// Build makes the generateContent call.
func (Images) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	base := strings.TrimRight(media.BaseURL(r), "/")
	if base == "" {
		return media.Call{}, media.BadRequestf("the provider names no address")
	}
	q := url.Values{}
	h := media.Headers(r, q)
	h["Content-Type"] = "application/json"
	body, err := json.Marshal(map[string]any{
		"contents": []map[string]any{{
			"role":  "user",
			"parts": []map[string]string{{"text": gjson.GetBytes(r.Body, "prompt").String()}},
		}},
		"generationConfig": map[string]any{"responseModalities": []string{"TEXT", "IMAGE"}},
	})
	if err != nil {
		return media.Call{}, media.BadRequestf("the request cannot be built")
	}
	model := "models/" + strings.TrimPrefix(r.WireModel, "models/")
	return media.Call{Method: http.MethodPost, URL: withQuery(base+"/"+model+":generateContent", q), Header: h, Body: body}, nil
}

// Parse reads the inline image parts into the OpenAI list.
func (Images) Parse(_ context.Context, r media.Request, _ media.Client, status int, _ http.Header, body []byte) (media.Response, error) {
	if status < 200 || status > 299 {
		return media.Response{}, &media.ProviderError{Status: status, Text: "the provider refused the image"}
	}
	data := []map[string]any{}
	for _, part := range gjson.GetBytes(body, "candidates.0.content.parts").Array() {
		if b := part.Get("inlineData.data").String(); b != "" {
			data = append(data, map[string]any{"b64_json": b})
		}
	}
	if len(data) == 0 {
		return media.Response{}, &media.ProviderError{Status: http.StatusBadGateway, Text: "the provider answered no image"}
	}
	out, _ := json.Marshal(map[string]any{"created": 0, "data": data})
	return media.Response{Status: http.StatusOK, Header: map[string]string{"Content-Type": "application/json"}, Body: out}, nil
}

func setDimensions(body []byte, d int64, _ int) ([]byte, error) {
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, err
	}
	if reqs, ok := obj["requests"].([]any); ok {
		for _, r := range reqs {
			r.(map[string]any)["outputDimensionality"] = d
		}
		return json.Marshal(obj)
	}
	obj["outputDimensionality"] = d
	return json.Marshal(obj)
}

func withQuery(base string, q url.Values) string {
	if len(q) == 0 {
		return base
	}
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	return base + sep + q.Encode()
}
