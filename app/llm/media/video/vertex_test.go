package video

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
)

const serviceAccount = `{"type":"service_account","project_id":"demo-project-1","client_email":"a@b.iam.gserviceaccount.com","private_key":"KEY"}`

type tokens struct{ seen upstream.CloudKey }

func (f *tokens) Token(_ context.Context, k upstream.CloudKey) (string, error) {
	f.seen = k
	return "TOKEN", nil
}

func vertexRequest() media.Request {
	return media.Request{
		Kind: upstream.KindVideo, Provider: "vertex", Credential: serviceAccount,
		Action: media.VideoGenerations, ContentType: "application/json",
		Body: []byte(`{"model":"veo-3.1-generate-preview","prompt":"a cat","image":"data:image/png;base64,QUJD",` +
			`"n":2,"duration":8,"aspect_ratio":"16:9","seed":7,"generate_audio":true}`),
		Config:  &upstream.MediaConfig{BaseURL: "https://aiplatform.googleapis.com"},
		Account: map[string]string{"projectId": "acct-project-1", "location": "europe-west4"},
	}
}

func TestVertexCreateMintsATokenAndMapsTheBody(t *testing.T) {
	f := &tokens{}
	call, err := (Vertex{Tokens: f}).Build(context.Background(), vertexRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "https://aiplatform.googleapis.com/v1/projects/acct-project-1/locations/europe-west4" +
		"/publishers/google/models/veo-3.1-generate-preview:predictLongRunning"
	if call.Method != http.MethodPost || call.URL != want {
		t.Fatalf("%s %s", call.Method, call.URL)
	}
	if call.Header["Authorization"] != "Bearer TOKEN" {
		t.Fatalf("%v", call.Header)
	}
	body := gjson.ParseBytes(call.Body)
	if body.Get("instances.0.prompt").String() != "a cat" ||
		body.Get("instances.0.image.bytesBase64Encoded").String() != "QUJD" ||
		body.Get("instances.0.image.mimeType").String() != "image/png" ||
		body.Get("parameters.sampleCount").Int() != 2 ||
		body.Get("parameters.durationSeconds").Int() != 8 ||
		body.Get("parameters.aspectRatio").String() != "16:9" ||
		body.Get("parameters.seed").Int() != 7 ||
		!body.Get("parameters.generateAudio").Bool() {
		t.Fatalf("%s", call.Body)
	}
	if f.seen.Kind != upstream.KeyServiceAccount || f.seen.Project != "demo-project-1" {
		t.Fatalf("%+v", f.seen)
	}
}

func TestVertexFallsBackToTheKeysProjectAndTheDefaultLocation(t *testing.T) {
	r := vertexRequest()
	r.Account = nil
	r.Body = []byte(`{"model":"veo-2.0-generate-001","prompt":"a cat"}`)
	call, err := (Vertex{Tokens: &tokens{}}).Build(context.Background(), r, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(call.URL, "/projects/demo-project-1/locations/us-central1/") {
		t.Fatalf("%s", call.URL)
	}
}

func TestVertexRefusesARawKey(t *testing.T) {
	r := vertexRequest()
	r.Credential = "plain-key"
	if _, err := (Vertex{Tokens: &tokens{}}).Build(context.Background(), r, nil); err == nil {
		t.Fatal("a raw key was accepted")
	}
}

func TestVertexRequiresAProject(t *testing.T) {
	r := vertexRequest()
	r.Credential = `{"type":"authorized_user","client_id":"c","client_secret":"s","refresh_token":"r"}`
	r.Account = nil
	if _, err := (Vertex{Tokens: &tokens{}}).Build(context.Background(), r, nil); err == nil {
		t.Fatal("an account with no project was accepted")
	}
}

func TestVertexPollsTheOperation(t *testing.T) {
	name := "projects/p/locations/l/publishers/google/models/m/operations/o"
	r := vertexRequest()
	r.JobID = encodeJob(name)
	call, err := (Vertex{Tokens: &tokens{}}).Build(context.Background(), r, nil)
	if err != nil {
		t.Fatal(err)
	}
	if call.Method != http.MethodPost ||
		call.URL != "https://aiplatform.googleapis.com/v1/projects/p/locations/l/publishers/google/models/m:fetchPredictOperation" {
		t.Fatalf("%s %s", call.Method, call.URL)
	}
	if gjson.GetBytes(call.Body, "operationName").String() != name {
		t.Fatalf("%s", call.Body)
	}
}

func TestVertexRefusesAnIDThatIsNotAnOperation(t *testing.T) {
	for _, id := range []string{
		"..",
		encodeJob("projects/p/locations/l/publishers/google/models/m"),
		encodeJob("../locations/l/publishers/google/models/m/operations/o"),
		encodeJob("xprojects/p/locations/l/publishers/google/models/m/operations/o"),
		encodeJob("projects/p/locations/l/publishers/google/models/m/operations/o") + "=",
		strings.Repeat("a", 1025),
	} {
		if _, ok := decodeJob(id); ok {
			t.Fatalf("%q was accepted", id)
		}
	}
}

func TestVertexReshapesTheOperation(t *testing.T) {
	name := "projects/p/locations/l/publishers/google/models/m/operations/o"
	id := encodeJob(name)
	cases := []struct {
		name string
		raw  string
		want func(t *testing.T, body []byte)
	}{
		{"pending", `{"name":"` + name + `"}`, func(t *testing.T, body []byte) {
			if gjson.GetBytes(body, "status").String() != "pending" || gjson.GetBytes(body, "request_id").String() != id {
				t.Fatalf("%s", body)
			}
		}},
		{"failed", `{"name":"` + name + `","error":{"code":400}}`, func(t *testing.T, body []byte) {
			if gjson.GetBytes(body, "status").String() != "failed" || gjson.GetBytes(body, "error.code").Int() != 400 {
				t.Fatalf("%s", body)
			}
		}},
		{"done", `{"name":"` + name + `","done":true,"response":{"generateVideoResponse":{"generatedSamples":[` +
			`{"video":{"uri":"https://v/1.mp4","mimeType":"video/mp4"}}]}}}`, func(t *testing.T, body []byte) {
			if gjson.GetBytes(body, "status").String() != "completed" ||
				gjson.GetBytes(body, "video.url").String() != "https://v/1.mp4" ||
				gjson.GetBytes(body, "videos.0.mime_type").String() != "video/mp4" {
				t.Fatalf("%s", body)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := (Vertex{}).Parse(context.Background(), media.Request{}, nil, 200, nil, []byte(c.raw))
			if err != nil {
				t.Fatal(err)
			}
			c.want(t, out.Body)
		})
	}
}

func TestVertexRefusesANonJSONAnswer(t *testing.T) {
	if _, err := (Vertex{}).Parse(context.Background(), media.Request{}, nil, 200, nil, []byte("nope")); err == nil {
		t.Fatal("a non-JSON answer was accepted")
	}
}

func FuzzVertexJobID(f *testing.F) {
	f.Add(encodeJob("projects/p/locations/l/publishers/google/models/m/operations/o"))
	f.Add("..")
	f.Fuzz(func(t *testing.T, id string) {
		name, ok := decodeJob(id)
		if !ok {
			return
		}
		if encodeJob(name) != id || !operationName.MatchString(name) {
			t.Fatalf("%q decoded to %q, which does not round-trip", id, name)
		}
	})
}
