package imaging

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
)

// cloudflareMultipart are the models Cloudflare takes as form data.
var cloudflareMultipart = map[string]bool{
	"@cf/black-forest-labs/flux-2-dev":      true,
	"@cf/black-forest-labs/flux-2-klein-4b": true,
	"@cf/black-forest-labs/flux-2-klein-9b": true,
}

// Cloudflare speaks Workers AI's run endpoint under the account id.
type Cloudflare struct{}

// Kind is the service it serves.
func (Cloudflare) Kind() string { return upstream.KindImage }

// Build makes the call, multipart for the models that need it.
func (Cloudflare) Build(ctx context.Context, r media.Request, c media.Client) (media.Call, error) {
	base := strings.TrimRight(media.BaseURL(r), "/")
	account := strings.TrimSpace(r.Account["accountId"])
	if base == "" || account == "" || strings.ContainsAny(account, "/?#") {
		return media.Call{}, media.BadRequestf("the connection names no account")
	}
	body := gjson.ParseBytes(r.Body)
	width, height := media.SizeToDimensions(body.Get("size").String())
	if w := intBody(body, "width"); w > 0 {
		width = w
	}
	if h := intBody(body, "height"); h > 0 {
		height = h
	}
	target := base + "/" + account + "/ai/run/" + r.WireModel
	headers := map[string]string{"Accept": "application/json"}
	if r.Credential != "" {
		headers["Authorization"] = "Bearer " + r.Credential
	}
	if cloudflareMultipart[r.WireModel] {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		_ = mw.WriteField("prompt", body.Get("prompt").String())
		if width > 0 {
			_ = mw.WriteField("width", strconv.Itoa(width))
			_ = mw.WriteField("height", strconv.Itoa(height))
		}
		_ = mw.Close()
		headers["Content-Type"] = mw.FormDataContentType()
		return media.Call{Method: http.MethodPost, URL: target, Header: headers, Body: buf.Bytes()}, nil
	}
	req := map[string]any{"prompt": body.Get("prompt").String()}
	if width > 0 {
		req["width"], req["height"] = width, height
	}
	for _, name := range []string{"negative_prompt", "guidance", "seed", "num_steps", "steps", "strength"} {
		if v := body.Get(name); v.Exists() && v.String() != "" {
			req[name] = v.Value()
		}
	}
	source := body.Get("image").String()
	if source == "" {
		source = body.Get("images.0").String()
	}
	if source != "" {
		b64, err := media.InlineImage(ctx, c, source)
		if err != nil {
			return media.Call{}, err
		}
		if raw, err := base64.StdEncoding.DecodeString(b64); err == nil {
			req["image_b64"] = b64
			req["image"] = raw
		}
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return media.Call{}, media.BadRequestf("the request cannot be built")
	}
	headers["Content-Type"] = "application/json"
	return media.Call{Method: http.MethodPost, URL: target, Header: headers, Body: raw}, nil
}

// Parse reads a binary image or Cloudflare's JSON envelope.
func (Cloudflare) Parse(_ context.Context, _ media.Request, _ media.Client, status int, header http.Header, body []byte) (media.Response, error) {
	if status < 200 || status > 299 {
		return media.Response{}, &media.ProviderError{Status: status, Text: "the provider refused the image"}
	}
	if strings.HasPrefix(header.Get("Content-Type"), "image/") {
		if b64, _ := media.BinaryImage(body); b64 != "" {
			return imageList([]string{b64}), nil
		}
		return media.Response{}, &media.ProviderError{Status: http.StatusBadGateway, Text: "the provider answered no image"}
	}
	if !gjson.ValidBytes(body) {
		return media.Response{}, &media.ProviderError{Status: http.StatusBadGateway, Text: "the provider answered no image"}
	}
	// The answer may nest a result under `result` or `result.responses[]`.
	node := gjson.GetBytes(body, "result")
	if !node.Exists() {
		node = gjson.ParseBytes(body)
	}
	if arr := node.Get("responses"); arr.IsArray() && len(arr.Array()) > 0 {
		for _, entry := range arr.Array() {
			if entry.Get("success").Type != gjson.False {
				node = entry
				break
			}
		}
	}
	var images []string
	switch {
	case node.Type == gjson.String && node.String() != "":
		images = append(images, node.String())
	case node.Get("image").String() != "":
		images = append(images, node.Get("image").String())
	case node.Get("data.0.b64_json").String() != "":
		images = append(images, node.Get("data.0.b64_json").String())
	case node.Get("data.0.url").String() != "":
		return urlList([]string{node.Get("data.0.url").String()}), nil
	}
	if len(images) == 0 {
		return media.Response{}, &media.ProviderError{Status: http.StatusBadGateway, Text: "the provider answered no image"}
	}
	return imageList(images), nil
}

func intBody(body gjson.Result, name string) int {
	v := body.Get(name)
	if v.Type != gjson.Number {
		return 0
	}
	return int(v.Int())
}
