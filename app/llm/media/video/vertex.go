package video

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
)

// TokenSource trades a Vertex key for a bearer token. The OAuth minter is the
// implementation; a test passes a fake.
type TokenSource interface {
	Token(ctx context.Context, key upstream.CloudKey) (string, error)
}

// defaultLocation is the region a Veo job runs in when the account names none.
const defaultLocation = "us-central1"

// vertexModel is a model id fit for one path segment.
var vertexModel = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// operationName is the resource path a Veo job is polled by, anchored so a
// decoded id can never carry a host change into the address.
var operationName = regexp.MustCompile(`^projects/[^/]+/locations/[^/]+/publishers/[^/]+/models/[^/]+/operations/[^/]+$`)

// Vertex speaks the Veo long-running-operation API, which is not the xAI shape
// in either direction: the create starts an operation, the poll reads it, and
// the operation is reshaped to the async job a client already polls for.
type Vertex struct{ Tokens TokenSource }

// Kind is the service it serves.
func (Vertex) Kind() string { return upstream.KindVideo }

// Build makes the create or the poll call.
func (v Vertex) Build(ctx context.Context, r media.Request, _ media.Client) (media.Call, error) {
	key, ok := upstream.ParseCloudKey(r.Credential)
	if !ok {
		return media.Call{}, media.BadRequestf("the account's Vertex key cannot be read")
	}
	if key.Kind == upstream.KeyRaw {
		return media.Call{}, media.BadRequestf("this provider takes a service account, not a raw key")
	}
	if v.Tokens == nil {
		return media.Call{}, media.BadRequestf("this provider has no token source")
	}
	token, err := v.Tokens.Token(ctx, key)
	if err != nil {
		return media.Call{}, fmt.Errorf("the account's Vertex token could not be minted: %w", err)
	}
	b, err := base(r)
	if err != nil {
		return media.Call{}, err
	}
	h := map[string]string{"Accept": "application/json", "Authorization": "Bearer " + token}
	if r.Config != nil {
		for k, val := range r.Config.Headers {
			h[k] = val
		}
	}

	if r.JobID != "" {
		name, ok := decodeJob(r.JobID)
		if !ok {
			return media.Call{}, media.BadRequestf("the job id is not one this router accepts")
		}
		body, err := json.Marshal(map[string]string{"operationName": name})
		if err != nil {
			return media.Call{}, media.BadRequestf("the request cannot be built")
		}
		h["Content-Type"] = "application/json"
		return media.Call{
			Method: http.MethodPost,
			URL:    b + "/v1/" + name[:strings.Index(name, "/operations/")] + ":fetchPredictOperation",
			Header: h, Body: body,
		}, nil
	}

	if r.Action != media.VideoGenerations {
		return media.Call{}, media.BadRequestf("this provider serves generations only")
	}
	if ct := strings.ToLower(r.ContentType); ct != "" && !strings.Contains(ct, "json") {
		return media.Call{}, media.BadRequestf("this provider takes a JSON body")
	}
	project, err := projectOf(r, key)
	if err != nil {
		return media.Call{}, err
	}
	body := gjson.ParseBytes(r.Body)
	model := body.Get("model").String()
	if model == "" {
		model = r.WireModel
	}
	if !vertexModel.MatchString(model) {
		return media.Call{}, media.BadRequestf("the request names no model this provider accepts")
	}
	if body.Get("prompt").String() == "" && body.Get("image").String() == "" && body.Get("image_url").String() == "" {
		return media.Call{}, media.BadRequestf("the request names no prompt or image")
	}
	wire, err := toVertexBody(body)
	if err != nil {
		return media.Call{}, err
	}
	location := defaultLocation
	if l := r.Account["location"]; upstream.ValidLocation(l) {
		location = l
	}
	h["Content-Type"] = "application/json"
	return media.Call{
		Method: http.MethodPost,
		URL: b + "/v1/projects/" + project + "/locations/" + location +
			"/publishers/google/models/" + model + ":predictLongRunning",
		Header: h, Body: wire,
	}, nil
}

// Parse reshapes the operation into the async job shape.
func (Vertex) Parse(_ context.Context, _ media.Request, _ media.Client, status int, _ http.Header, body []byte) (media.Response, error) {
	if status < 200 || status > 299 {
		return media.Response{}, &media.ProviderError{Status: status, Text: "the provider refused the video"}
	}
	if !gjson.ValidBytes(body) {
		return media.Response{}, &media.ProviderError{Status: http.StatusBadGateway, Text: "the provider answered no job"}
	}
	out, err := fromOperation(body)
	if err != nil {
		return media.Response{}, &media.ProviderError{Status: http.StatusBadGateway, Text: "the provider answered no job"}
	}
	return media.Response{Status: status, Header: map[string]string{"Content-Type": "application/json"}, Body: out}, nil
}

// projectOf is the project the job runs in: the account's own, then the key's.
func projectOf(r media.Request, key upstream.CloudKey) (string, error) {
	if p := r.Account["projectId"]; upstream.ValidProject(p) {
		return p, nil
	}
	if upstream.ValidProject(key.Project) {
		return key.Project, nil
	}
	return "", media.BadRequestf("the account names no project")
}

// toVertexBody maps the client's video body onto the provider's two objects.
func toVertexBody(body gjson.Result) ([]byte, error) {
	instance := map[string]any{}
	if prompt := body.Get("prompt").String(); prompt != "" {
		instance["prompt"] = prompt
	}
	switch image := body.Get("image"); {
	case image.IsObject():
		instance["image"] = json.RawMessage(image.Raw)
	case image.String() != "":
		instance["image"] = vertexImage(image.String())
	case body.Get("image_url").IsObject():
		instance["image"] = json.RawMessage(body.Get("image_url").Raw)
	case body.Get("image_url").String() != "":
		instance["image"] = vertexImage(body.Get("image_url").String())
	}
	if video := body.Get("video"); video.IsObject() {
		instance["video"] = json.RawMessage(video.Raw)
	}

	parameters := map[string]any{}
	for field, name := range map[string]string{
		"n": "sampleCount", "duration": "durationSeconds", "aspect_ratio": "aspectRatio",
		"resolution": "resolution", "seed": "seed", "negative_prompt": "negativePrompt",
		"storage_uri": "storageUri",
	} {
		v := body.Get(field)
		if v.Exists() && v.Type != gjson.Null {
			parameters[name] = v.Value()
		}
	}
	if v := body.Get("generate_audio"); v.Exists() && v.Type != gjson.Null {
		parameters["generateAudio"] = v.Bool()
	}

	wire := map[string]any{"instances": []any{instance}}
	if len(parameters) > 0 {
		wire["parameters"] = parameters
	}
	out, err := json.Marshal(wire)
	if err != nil {
		return nil, media.BadRequestf("the request cannot be built")
	}
	return out, nil
}

// vertexImage is an inline image or a bucket address, whichever the client sent.
func vertexImage(ref string) map[string]string {
	if strings.HasPrefix(ref, "data:") {
		meta, data, ok := strings.Cut(strings.TrimPrefix(ref, "data:"), ",")
		if ok {
			mime, _, _ := strings.Cut(meta, ";")
			return map[string]string{"bytesBase64Encoded": data, "mimeType": mime}
		}
	}
	return map[string]string{"gcsUri": ref}
}

// sample is one generated video.
type sample struct {
	URL  string `json:"url"`
	B64  string `json:"b64_json,omitempty"`
	Mime string `json:"mime_type,omitempty"`
}

// job is the async shape a client polls for.
type job struct {
	ID        string          `json:"id,omitempty"`
	RequestID string          `json:"request_id,omitempty"`
	Status    string          `json:"status"`
	Error     json.RawMessage `json:"error,omitempty"`
	Video     *sample         `json:"video,omitempty"`
	Videos    []sample        `json:"videos,omitempty"`
}

// fromOperation reshapes one Veo operation.
func fromOperation(raw []byte) ([]byte, error) {
	op := gjson.ParseBytes(raw)
	name := op.Get("name").String()
	if name == "" {
		return nil, errors.New("the operation names no job")
	}
	id := encodeJob(name)
	out := job{ID: id, RequestID: id, Status: "pending"}
	if e := op.Get("error"); e.Exists() && e.Type != gjson.Null {
		out.Status, out.Error = "failed", json.RawMessage(e.Raw)
		return json.Marshal(out)
	}
	if !op.Get("done").Bool() {
		return json.Marshal(out)
	}
	videos := samplesOf(op)
	out.Status = "completed"
	out.Videos = videos
	if len(videos) > 0 {
		out.Video = &videos[0]
	}
	return json.Marshal(out)
}

// samplesOf reads the generated samples from either response shape the model
// family uses.
func samplesOf(op gjson.Result) []sample {
	list := op.Get("response.videos")
	if !list.IsArray() {
		list = op.Get("response.generateVideoResponse.generatedSamples")
	}
	var out []sample
	for _, s := range list.Array() {
		v := sample{Mime: "video/mp4"}
		for _, pick := range []struct {
			dst *string
			key string
		}{
			{&v.URL, "gcsUri"}, {&v.URL, "video.uri"}, {&v.URL, "uri"},
			{&v.B64, "bytesBase64Encoded"}, {&v.B64, "video.bytesBase64Encoded"},
			{&v.Mime, "mimeType"}, {&v.Mime, "video.mimeType"},
		} {
			if got := s.Get(pick.key).String(); got != "" {
				*pick.dst = got
			}
		}
		out = append(out, v)
	}
	return out
}

// encodeJob is a resource path as one flat path segment.
func encodeJob(name string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(name))
}

// decodeJob reads a job id back, accepting only text that re-encodes to the
// same id and names an operation this router can poll.
func decodeJob(id string) (string, bool) {
	if id == "" || len(id) > 1024 {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil {
		return "", false
	}
	name := string(raw)
	if encodeJob(name) != id || !operationName.MatchString(name) {
		return "", false
	}
	return name, true
}
